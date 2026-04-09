package v2raygrpclite

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/tls"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/v2rayhttp"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.org/x/net/http2"
)

var _ adapter.V2RayClientTransport = (*Client)(nil)

var defaultClientHeader = http.Header{
	"Content-Type": []string{"application/grpc"},
	"User-Agent":   []string{"grpc-go/1.48.0"},
	"TE":           []string{"trailers"},
}

type Transport struct {
	transport *http2.Transport
	count     atomic.Int64
	lastUsed  atomic.Int64
}

type Client struct {
	ctx        context.Context
	serverAddr M.Socksaddr
	options    option.V2RayGRPCOptions
	url        *url.URL
	host       string

	transports      []*Transport
	mutex           sync.Mutex
	maker           func() *Transport
	threshold       int64
	idleTimeout     time.Duration
	cleanupInterval time.Duration
	cleanupTicker   *time.Ticker
	useCleanup      bool
	done            chan struct{}
}

func NewClient(ctx context.Context, dialer N.Dialer, serverAddr M.Socksaddr, options option.V2RayGRPCOptions, tlsConfig tls.Config) adapter.V2RayClientTransport {
	var host string
	if tlsConfig != nil && tlsConfig.ServerName() != "" {
		host = M.ParseSocksaddrHostPort(tlsConfig.ServerName(), serverAddr.Port).String()
	} else {
		host = serverAddr.String()
	}
	if options.MaxConnections == 0 && options.MinStreams == 0 && options.MaxStreams == 0 {
		options.MaxConnections = 1
	}
	client := &Client{
		ctx:        ctx,
		serverAddr: serverAddr,
		options:    options,
		url: &url.URL{
			Scheme:  "https",
			Host:    serverAddr.String(),
			Path:    "/" + options.ServiceName + "/Tun",
			RawPath: "/" + url.PathEscape(options.ServiceName) + "/Tun",
		},
		host: host,
		maker: func() *Transport {
			transport := &http2.Transport{
				IdleConnTimeout:    C.GRPCTransportIdleConnTimeout,
				ReadIdleTimeout:    time.Duration(options.IdleTimeout),
				PingTimeout:        time.Duration(options.PingTimeout),
				DisableCompression: true,
			}
			if tlsConfig == nil {
				transport.DialTLSContext = func(ctx context.Context, network, addr string, cfg *tls.STDConfig) (net.Conn, error) {
					return dialer.DialContext(ctx, network, M.ParseSocksaddr(addr))
				}
			} else {
				if len(tlsConfig.NextProtos()) == 0 {
					tlsConfig.SetNextProtos([]string{http2.NextProtoTLS})
				}
				tlsDialer := tls.NewDialer(dialer, tlsConfig)
				transport.DialTLSContext = func(ctx context.Context, network, addr string, cfg *tls.STDConfig) (net.Conn, error) {
					return tlsDialer.DialTLSContext(ctx, M.ParseSocksaddr(addr))
				}
			}
			return &Transport{transport: transport}
		},
		idleTimeout:     C.GRPCTransportIdleConnTimeout,
		cleanupInterval: C.GRPCTransportCleanUpInterval,
	}
	if options.MaxConnections > 0 {
		if options.MinStreams > 1 {
			client.threshold = int64(options.MinStreams)
		}
	} else if client.options.MaxStreams > 0 {
		client.threshold = int64(options.MaxStreams)
	}
	if client.threshold < 1 {
		client.threshold = 1
	}
	client.useCleanup = client.cleanupInterval > 0 && options.MaxConnections > 1 || (options.MaxConnections == 0 && options.MaxStreams > 0)
	client.startCleanup()
	return client
}

func (c *Client) newTransportLocked() *Transport {
	transport := c.maker()
	c.transports = append(c.transports, transport)
	return transport
}

func (c *Client) getTransport() *Transport {
	shouldCleanup := false
	if c.useCleanup {
		defer func() {
			if shouldCleanup {
				c.startCleanup()
			}
		}()
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.options.MaxConnections == 1 {
		if len(c.transports) == 0 {
			return c.newTransportLocked()
		}
		return c.transports[0]
	}
	// First pass: Look for any transport that still has capacity
	for _, t := range c.transports {
		count := t.count.Load()
		if count == 0 || count < c.threshold {
			return t
		}
	}
	if c.options.MaxConnections > 0 && len(c.transports) >= c.options.MaxConnections {
		// Hard limit reached → pick the least loaded for load balancing
		var best *Transport
		for _, t := range c.transports {
			if best == nil || t.count.Load() < best.count.Load() {
				best = t
			}
		}
		return best
	}
	if c.useCleanup && c.cleanupTicker == nil {
		shouldCleanup = true
	}
	return c.newTransportLocked()
}

func (c *Client) startCleanup() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.cleanupTicker != nil {
		return
	}
	ticker := time.NewTicker(c.cleanupInterval)
	done := make(chan struct{})
	c.cleanupTicker = ticker
	c.done = done
	go func() {
		for {
			select {
			case <-ticker.C:
				if c.doCleanup() {
					c.stopCleanup()
				}
			case <-done:
				return
			}
		}
	}()
}

func (c *Client) stopCleanup() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.cleanupTicker != nil {
		c.cleanupTicker.Stop()
		c.cleanupTicker = nil
	}
	if c.done != nil {
		close(c.done)
		c.done = nil
	}
}

func (c *Client) doCleanup() bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if len(c.transports) == 0 {
		return true
	}
	now := time.Now().UnixNano()
	threshold := c.idleTimeout.Nanoseconds()
	keep := c.transports[:0]
	for _, t := range c.transports {
		active := t.count.Load()
		idleNanos := now - t.lastUsed.Load()
		if active == 0 && idleNanos >= threshold {
			v2rayhttp.ResetTransport(t.transport)
			continue
		}
		keep = append(keep, t)
	}
	c.transports = keep
	return len(c.transports) == 0
}

func (c *Client) DialContext(ctx context.Context) (net.Conn, error) {
	pipeInReader, pipeInWriter := io.Pipe()
	request := &http.Request{
		Method: http.MethodPost,
		Body:   pipeInReader,
		URL:    c.url,
		Header: defaultClientHeader,
		Host:   c.host,
	}
	request = request.WithContext(ctx)
	conn := newLateGunConn(pipeInWriter)
	transport := c.getTransport()
	transport.count.Add(1)
	transport.lastUsed.Store(time.Now().UnixNano())
	conn.onClose = func() {
		transport.count.Add(-1)
		transport.lastUsed.Store(time.Now().UnixNano())
	}
	go func() {
		response, err := transport.transport.RoundTrip(request)
		if err != nil {
			conn.setup(nil, err)
		} else if response.StatusCode != 200 {
			response.Body.Close()
			conn.setup(nil, E.New("v2ray-grpc: unexpected status: ", response.Status))
		} else {
			conn.setup(response.Body, nil)
		}
	}()
	return conn, nil
}

func (c *Client) Close() error {
	if c.useCleanup {
		c.stopCleanup()
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for _, t := range c.transports {
		v2rayhttp.ResetTransport(t.transport)
	}
	c.transports = nil
	return nil
}
