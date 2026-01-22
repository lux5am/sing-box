package dns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/compatible"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/task"
	"github.com/sagernet/sing/contrab/freelru"
	"github.com/sagernet/sing/contrab/maphash"

	"github.com/miekg/dns"
)

var (
	ErrNoRawSupport           = E.New("no raw query support by current transport")
	ErrNotCached              = E.New("not cached")
	ErrResponseRejected       = E.New("response rejected")
	ErrResponseRejectedCached = E.Extend(ErrResponseRejected, "cached")
)

var _ adapter.DNSClient = (*Client)(nil)

type dnsAnswer struct {
	mu  sync.Mutex
	rra []dns.RR
	rr4 []dns.RR
	rr6 []dns.RR
}

func (rs *dnsAnswer) RoundRobin() []dns.RR {
	answer := make([]dns.RR, len(rs.rra), len(rs.rra)+len(rs.rr4)+len(rs.rr6))
	copy(answer, rs.rra)
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.rr4) > 0 {
		rr4 := make([]dns.RR, 0, len(rs.rr4));
		rr4 = append(rr4, rs.rr4[len(rs.rr4)-1])
		if len(rs.rr4) > 1 {
			rr4 = append(rr4, rs.rr4[:len(rs.rr4)-1]...)
		}
		answer = append(answer, rr4...)
		rs.rr4 = rr4
	}
	if len(rs.rr6) > 0 {
		rr6 := make([]dns.RR, 0, len(rs.rr6));
		rr6 = append(rr6, rs.rr6[len(rs.rr6)-1])
		if len(rs.rr6) > 1 {
			rr6 = append(rr6, rs.rr6[:len(rs.rr6)-1]...)
		}
		answer = append(answer, rr6...)
		rs.rr6 = rr6
	}
	return answer
}

type dnsMsg struct {
	msg      *dns.Msg
	rrs      *dnsAnswer
	expire   int64
	updating atomic.Bool
}

func (dm *dnsMsg) GetExpire() time.Time {
	return time.UnixMilli(dm.expire)
}

func (dm *dnsMsg) SetExpire(expire time.Time) {
	dm.expire = expire.UnixMilli()
}

func (dm *dnsMsg) Copy() *dns.Msg {
	msg := dm.msg.Copy()
	if dm.rrs != nil {
		msg.Answer = dm.rrs.RoundRobin()
	}
	return msg
}

func (c *Client) newDnsMsg(msg *dns.Msg) *dnsMsg {
	dMsg := &dnsMsg{msg: msg.Copy()}
	if !c.cacheRoundRobin {
		return dMsg
	}
	var (
		rra []dns.RR
		rr4 []dns.RR
		rr6 []dns.RR
	)
	for _, ans := range msg.Answer {
		switch a := ans.(type) {
		case *dns.A:
			rr4 = append(rr4, a)
		case *dns.AAAA:
			rr6 = append(rr6, a)
		default:
			rra = append(rra, a)
		}
	}
	if len(rr4) > 1 || len(rr6) > 1 {
		dMsg.msg.Answer = nil
		dMsg.rrs = &dnsAnswer{
			rra: rra,
			rr4: rr4,
			rr6: rr6,
		}
	}
	return dMsg
}

type Client struct {
	timeout            time.Duration
	disableCache       bool
	disableExpire      bool
	independentCache   bool
	cacheRoundRobin    bool
	cacheMinTTL        uint32
	cacheMaxTTL        uint32
	cacheStaleTTL      uint32
	cacheUseStaleTTL   bool
	clientSubnet       netip.Prefix
	rdrc               adapter.RDRCStore
	initRDRCFunc       func() adapter.RDRCStore
	logger             logger.ContextLogger
	cache              freelru.Cache[dns.Question, *dnsMsg]
	cacheLock          compatible.Map[dns.Question, chan struct{}]
	transportCache     freelru.Cache[transportCacheKey, *dnsMsg]
	transportCacheLock compatible.Map[dns.Question, chan struct{}]
}

type ClientOptions struct {
	Timeout          time.Duration
	DisableCache     bool
	DisableExpire    bool
	IndependentCache bool
	CacheRoundRobin  bool
	CacheCapacity    uint32
	ClientSubnet     netip.Prefix
	CacheMinTTL      uint32
	CacheMaxTTL      uint32
	CacheStaleTTL    uint32
	RDRC             func() adapter.RDRCStore
	Logger           logger.ContextLogger
}

func NewClient(options ClientOptions) *Client {
	client := &Client{
		timeout:          options.Timeout,
		disableCache:     options.DisableCache,
		disableExpire:    options.DisableExpire,
		independentCache: options.IndependentCache,
		cacheRoundRobin:  options.CacheRoundRobin,
		clientSubnet:     options.ClientSubnet,
		cacheMinTTL:      options.CacheMinTTL,
		cacheMaxTTL:      options.CacheMaxTTL,
		cacheStaleTTL:    options.CacheStaleTTL,
		cacheUseStaleTTL: options.CacheStaleTTL > 0,
		initRDRCFunc:     options.RDRC,
		logger:           options.Logger,
	}
	if client.cacheMaxTTL == 0 {
		client.cacheMaxTTL = 86400
	}
	if client.cacheMinTTL > client.cacheMaxTTL {
		client.cacheMaxTTL = client.cacheMinTTL
	}
	if client.timeout == 0 {
		client.timeout = C.DNSTimeout
	}
	cacheCapacity := options.CacheCapacity
	if cacheCapacity < 1024 {
		cacheCapacity = 1024
	}
	if !client.disableCache {
		if !client.independentCache {
			client.cache = common.Must1(freelru.NewSharded[dns.Question, *dnsMsg](cacheCapacity, maphash.NewHasher[dns.Question]().Hash32))
		} else {
			client.transportCache = common.Must1(freelru.NewSharded[transportCacheKey, *dnsMsg](cacheCapacity, maphash.NewHasher[transportCacheKey]().Hash32))
		}
	}
	return client
}

type transportCacheKey struct {
	dns.Question
	transportTag string
}

func (c *Client) Start() {
	if c.initRDRCFunc != nil {
		c.rdrc = c.initRDRCFunc()
	}
}

func extractNegativeTTL(response *dns.Msg) (uint32, bool) {
	for _, record := range response.Ns {
		if soa, isSOA := record.(*dns.SOA); isSOA {
			soaTTL := soa.Header().Ttl
			soaMinimum := soa.Minttl
			if soaTTL < soaMinimum {
				return soaTTL, true
			}
			return soaMinimum, true
		}
	}
	return 0, false
}

func (c *Client) Exchange(ctx context.Context, transport adapter.DNSTransport, message *dns.Msg, options adapter.DNSQueryOptions, responseChecker func(responseAddrs []netip.Addr) bool) (*dns.Msg, error) {
	if len(message.Question) == 0 {
		if c.logger != nil {
			c.logger.WarnContext(ctx, "bad question size: ", len(message.Question))
		}
		return FixedResponseStatus(message, dns.RcodeFormatError), nil
	}
	question := message.Question[0]
	if question.Qtype == dns.TypeA && options.Strategy == C.DomainStrategyIPv6Only || question.Qtype == dns.TypeAAAA && options.Strategy == C.DomainStrategyIPv4Only {
		if c.logger != nil {
			c.logger.DebugContext(ctx, "strategy rejected")
		}
		return FixedResponseStatus(message, dns.RcodeSuccess), nil
	}
	clientSubnet := options.ClientSubnet
	if !clientSubnet.IsValid() {
		clientSubnet = c.clientSubnet
	}
	if clientSubnet.IsValid() {
		message = SetClientSubnet(message, clientSubnet)
	}

	isSimpleRequest := len(message.Question) == 1 &&
		len(message.Ns) == 0 &&
		(len(message.Extra) == 0 || len(message.Extra) == 1 &&
			message.Extra[0].Header().Rrtype == dns.TypeOPT &&
			message.Extra[0].Header().Class > 0 &&
			message.Extra[0].Header().Ttl == 0 &&
			len(message.Extra[0].(*dns.OPT).Option) == 0) &&
		!options.ClientSubnet.IsValid()
	disableCache := !isSimpleRequest || c.disableCache || options.DisableCache
	if !disableCache && !options.ExchangeWithoutCache {
		if c.cache != nil {
			cond, loaded := c.cacheLock.LoadOrStore(question, make(chan struct{}))
			if loaded {
				select {
				case <-cond:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			} else {
				defer func() {
					c.cacheLock.Delete(question)
					close(cond)
				}()
			}
		} else if c.transportCache != nil {
			cond, loaded := c.transportCacheLock.LoadOrStore(question, make(chan struct{}))
			if loaded {
				select {
				case <-cond:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			} else {
				defer func() {
					c.transportCacheLock.Delete(question)
					close(cond)
				}()
			}
		}
		cacheM, stale := c.loadResponse(question, transport)
		if cacheM != nil {
			response := cacheM.Copy()
			nowTTL := uint32(time.Until(cacheM.GetExpire()).Seconds())
			if !c.disableExpire {
				if stale {
					setMsgTTL(response, 0)
					addMsgStaleAnswerOpt(response)
					if cacheM.updating.CompareAndSwap(false, true) {
						options.ExchangeWithoutCache = true
						c.logger.DebugContext(ctx, "updating stale cache ", question.Name)
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), C.DNSTimeout)
							defer cancel()
							_, _ = c.Exchange(ctx, transport, message, options, responseChecker)
						}()
					}
				} else {
					updateMsgTTL(response, nowTTL)
				}
			}
			logCachedResponse(c.logger, ctx, response, int(nowTTL))
			response.Id = message.Id
			return response, nil
		}
	}

	messageId := message.Id
	contextTransport, clientSubnetLoaded := transportTagFromContext(ctx)
	if clientSubnetLoaded && transport.Tag() == contextTransport {
		return nil, E.New("DNS query loopback in transport[", contextTransport, "]")
	}
	ctx = contextWithTransportTag(ctx, transport.Tag())
	if !disableCache && responseChecker != nil && c.rdrc != nil {
		rejected := c.rdrc.LoadRDRC(transport.Tag(), question.Name, question.Qtype)
		if rejected {
			return nil, ErrResponseRejectedCached
		}
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	response, err := transport.Exchange(ctx, message)
	cancel()
	if err != nil {
		var rcodeError RcodeError
		if errors.As(err, &rcodeError) {
			response = FixedResponseStatus(message, int(rcodeError))
		} else {
			return nil, err
		}
	}
	/*if question.Qtype == dns.TypeA || question.Qtype == dns.TypeAAAA {
		validResponse := response
	loop:
		for {
			var (
				addresses  int
				queryCNAME string
			)
			for _, rawRR := range validResponse.Answer {
				switch rr := rawRR.(type) {
				case *dns.A:
					break loop
				case *dns.AAAA:
					break loop
				case *dns.CNAME:
					queryCNAME = rr.Target
				}
			}
			if queryCNAME == "" {
				break
			}
			exMessage := *message
			exMessage.Question = []dns.Question{{
				Name:  queryCNAME,
				Qtype: question.Qtype,
			}}
			validResponse, err = c.Exchange(ctx, transport, &exMessage, options, responseChecker)
			if err != nil {
				return nil, err
			}
		}
		if validResponse != response {
			response.Answer = append(response.Answer, validResponse.Answer...)
		}
	}*/
	disableCache = disableCache || (response.Rcode != dns.RcodeSuccess && response.Rcode != dns.RcodeNameError)
	if responseChecker != nil {
		var rejected bool
		// TODO: add accept_any rule and support to check response instead of addresses
		if response.Rcode != dns.RcodeSuccess || len(response.Answer) == 0 {
			rejected = true
		} else {
			rejected = !responseChecker(MessageToAddresses(response))
		}
		if rejected {
			if !disableCache && c.rdrc != nil {
				c.rdrc.SaveRDRCAsync(transport.Tag(), question.Name, question.Qtype, c.logger)
			}
			logRejectedResponse(c.logger, ctx, response)
			return response, ErrResponseRejected
		}
	}
	if question.Qtype == dns.TypeHTTPS {
		if options.Strategy == C.DomainStrategyIPv4Only || options.Strategy == C.DomainStrategyIPv6Only {
			for _, rr := range response.Answer {
				https, isHTTPS := rr.(*dns.HTTPS)
				if !isHTTPS {
					continue
				}
				content := https.SVCB
				content.Value = common.Filter(content.Value, func(it dns.SVCBKeyValue) bool {
					if options.Strategy == C.DomainStrategyIPv4Only {
						return it.Key() != dns.SVCB_IPV6HINT
					} else {
						return it.Key() != dns.SVCB_IPV4HINT
					}
				})
				https.SVCB = content
			}
		}
	}
	var timeToLive uint32
	if len(response.Answer) == 0 {
		if soaTTL, hasSOA := extractNegativeTTL(response); hasSOA {
			timeToLive = soaTTL
		}
	}
	if timeToLive == 0 {
		for _, recordList := range [][]dns.RR{response.Answer, response.Ns, response.Extra} {
			for _, record := range recordList {
				if timeToLive == 0 || record.Header().Ttl > 0 && record.Header().Ttl < timeToLive {
					timeToLive = record.Header().Ttl
				}
			}
		}
	}
	if options.RewriteTTL != nil {
		timeToLive = *options.RewriteTTL
	}
	for _, recordList := range [][]dns.RR{response.Answer, response.Ns, response.Extra} {
		for _, record := range recordList {
			record.Header().Ttl = timeToLive
		}
	}
	if !disableCache {
		if options.RewriteTTL == nil && (c.cacheMinTTL > 0 || c.cacheMaxTTL > 0) {
			ttl := timeToLive
			resp := response
			if ttl < c.cacheMinTTL {
				ttl = c.cacheMinTTL
			}
			if ttl > c.cacheMaxTTL {
				ttl = c.cacheMaxTTL
			}
			if ttl != timeToLive {
				resp = response.Copy()
				for _, recordList := range [][]dns.RR{resp.Answer, resp.Ns, resp.Extra} {
					for _, record := range recordList {
						record.Header().Ttl = ttl
					}
				}
			}
			c.storeCache(transport, question, resp, ttl)
		} else {
			c.storeCache(transport, question, response, timeToLive)
		}
	}
	response.Id = messageId
	requestEDNSOpt := message.IsEdns0()
	responseEDNSOpt := response.IsEdns0()
	if responseEDNSOpt != nil && (requestEDNSOpt == nil || requestEDNSOpt.Version() < responseEDNSOpt.Version()) {
		response.Extra = common.Filter(response.Extra, func(it dns.RR) bool {
			return it.Header().Rrtype != dns.TypeOPT
		})
		if requestEDNSOpt != nil {
			response.SetEdns0(responseEDNSOpt.UDPSize(), responseEDNSOpt.Do())
		}
	}
	logExchangedResponse(c.logger, ctx, response, timeToLive)
	return response, nil
}

func (c *Client) Lookup(ctx context.Context, transport adapter.DNSTransport, domain string, options adapter.DNSQueryOptions, responseChecker func(responseAddrs []netip.Addr) bool) ([]netip.Addr, error) {
	domain = FqdnToDomain(domain)
	dnsName := dns.Fqdn(domain)
	var strategy C.DomainStrategy
	if options.LookupStrategy != C.DomainStrategyAsIS {
		strategy = options.LookupStrategy
	} else {
		strategy = options.Strategy
	}
	if strategy == C.DomainStrategyIPv4Only {
		return c.lookupToExchange(ctx, transport, dnsName, dns.TypeA, options, responseChecker)
	} else if strategy == C.DomainStrategyIPv6Only {
		return c.lookupToExchange(ctx, transport, dnsName, dns.TypeAAAA, options, responseChecker)
	}
	var response4 []netip.Addr
	var response6 []netip.Addr
	var group task.Group
	group.Append("exchange4", func(ctx context.Context) error {
		response, err := c.lookupToExchange(ctx, transport, dnsName, dns.TypeA, options, responseChecker)
		if err != nil {
			return err
		}
		response4 = response
		return nil
	})
	group.Append("exchange6", func(ctx context.Context) error {
		response, err := c.lookupToExchange(ctx, transport, dnsName, dns.TypeAAAA, options, responseChecker)
		if err != nil {
			return err
		}
		response6 = response
		return nil
	})
	err := group.Run(ctx)
	if len(response4) == 0 && len(response6) == 0 {
		return nil, err
	}
	return sortAddresses(response4, response6, strategy), nil
}

func (c *Client) ClearCache() {
	if c.cache != nil {
		c.cache.Purge()
	} else if c.transportCache != nil {
		c.transportCache.Purge()
	}
}

func sortAddresses(response4 []netip.Addr, response6 []netip.Addr, strategy C.DomainStrategy) []netip.Addr {
	if strategy == C.DomainStrategyPreferIPv6 {
		return append(response6, response4...)
	} else {
		return append(response4, response6...)
	}
}

func (c *Client) storeCache(transport adapter.DNSTransport, question dns.Question, message *dns.Msg, timeToLive uint32) {
	if timeToLive == 0 {
		return
	}
	if c.disableExpire {
		if !c.independentCache {
			c.cache.Add(question, c.newDnsMsg(message))
		} else {
			c.transportCache.Add(transportCacheKey{
				Question:     question,
				transportTag: transport.Tag(),
			}, c.newDnsMsg(message))
		}
	} else {
		cacheM := c.newDnsMsg(message)
		lifetime := time.Second * time.Duration(timeToLive)
		cacheM.SetExpire(time.Now().Add(lifetime))
		if c.cacheUseStaleTTL {
			lifetime = lifetime + (time.Second * time.Duration(c.cacheStaleTTL))
		}
		if !c.independentCache {
			c.cache.AddWithLifetime(question, cacheM, lifetime)
		} else {
			c.transportCache.AddWithLifetime(transportCacheKey{
				Question:     question,
				transportTag: transport.Tag(),
			}, cacheM, lifetime)
		}
	}
}

func (c *Client) lookupToExchange(ctx context.Context, transport adapter.DNSTransport, name string, qType uint16, options adapter.DNSQueryOptions, responseChecker func(responseAddrs []netip.Addr) bool) ([]netip.Addr, error) {
	question := dns.Question{
		Name:   name,
		Qtype:  qType,
		Qclass: dns.ClassINET,
	}
	disableCache := c.disableCache || options.DisableCache
	if !disableCache {
		cacheM, stale := c.loadResponse(question, transport)
		if cacheM != nil {
			if !c.disableExpire {
				if stale {
					if cacheM.updating.CompareAndSwap(false, true) {
						options.ExchangeWithoutCache = true
						message := &dns.Msg{
							MsgHdr: dns.MsgHdr{
								RecursionDesired: true,
							},
							Question: []dns.Question{question},
						}
						c.logger.DebugContext(ctx, "updating stale cache ", question.Name)
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), C.DNSTimeout)
							defer cancel()
							_, _ = c.Exchange(ctx, transport, message, options, responseChecker)
						}()
					}
				}
			}
			return MessageToAddresses(cacheM.Copy()), nil
		}
	}
	message := dns.Msg{
		MsgHdr: dns.MsgHdr{
			RecursionDesired: true,
		},
		Question: []dns.Question{question},
	}
	response, err := c.Exchange(ctx, transport, &message, options, responseChecker)
	if err != nil {
		return nil, err
	}
	if response.Rcode != dns.RcodeSuccess {
		return nil, RcodeError(response.Rcode)
	}
	return MessageToAddresses(response), nil
}

func (c *Client) questionCache(question dns.Question, transport adapter.DNSTransport) ([]netip.Addr, error) {
	response, _ := c.loadResponse(question, transport)
	if response == nil {
		return nil, ErrNotCached
	}
	if response.msg.Rcode != dns.RcodeSuccess {
		return nil, RcodeError(response.msg.Rcode)
	}
	return MessageToAddresses(response.msg), nil
}

func (c *Client) loadResponse(question dns.Question, transport adapter.DNSTransport) (*dnsMsg, bool) {
	var (
		cacheM   *dnsMsg
		loaded   bool
	)
	if c.disableExpire {
		if !c.independentCache {
			cacheM, loaded = c.cache.Get(question)
		} else {
			cacheM, loaded = c.transportCache.Get(transportCacheKey{
				Question:     question,
				transportTag: transport.Tag(),
			})
		}
		if !loaded {
			return nil, false
		}
		return cacheM, false
	} else {
		var expireAt time.Time
		if !c.independentCache {
			cacheM, expireAt, loaded = c.cache.GetWithLifetime(question)
		} else {
			cacheM, expireAt, loaded = c.transportCache.GetWithLifetime(transportCacheKey{
				Question:     question,
				transportTag: transport.Tag(),
			})
		}
		if !loaded {
			return nil, false
		}
		timeNow := time.Now()
		if timeNow.After(expireAt) {
			if !c.independentCache {
				c.cache.Remove(question)
			} else {
				c.transportCache.Remove(transportCacheKey{
					Question:     question,
					transportTag: transport.Tag(),
				})
			}
			return nil, false
		}
		return cacheM, c.cacheUseStaleTTL && timeNow.After(cacheM.GetExpire())
	}
}

func MessageToAddresses(response *dns.Msg) []netip.Addr {
	if response == nil || response.Rcode != dns.RcodeSuccess {
		return nil
	}
	addresses := make([]netip.Addr, 0, len(response.Answer))
	for _, rawAnswer := range response.Answer {
		switch answer := rawAnswer.(type) {
		case *dns.A:
			addresses = append(addresses, M.AddrFromIP(answer.A))
		case *dns.AAAA:
			addresses = append(addresses, M.AddrFromIP(answer.AAAA))
		case *dns.HTTPS:
			for _, value := range answer.SVCB.Value {
				if value.Key() == dns.SVCB_IPV4HINT || value.Key() == dns.SVCB_IPV6HINT {
					addresses = append(addresses, common.Map(strings.Split(value.String(), ","), M.ParseAddr)...)
				}
			}
		}
	}
	return addresses
}

func wrapError(err error) error {
	switch dnsErr := err.(type) {
	case *net.DNSError:
		if dnsErr.IsNotFound {
			return RcodeNameError
		}
	case *net.AddrError:
		return RcodeNameError
	}
	return err
}

type transportKey struct{}

func contextWithTransportTag(ctx context.Context, transportTag string) context.Context {
	return context.WithValue(ctx, transportKey{}, transportTag)
}

func transportTagFromContext(ctx context.Context) (string, bool) {
	value, loaded := ctx.Value(transportKey{}).(string)
	return value, loaded
}

func FixedResponseStatus(message *dns.Msg, rcode int) *dns.Msg {
	return &dns.Msg{
		MsgHdr: dns.MsgHdr{
			Id:                 message.Id,
			Response:           true,
			Authoritative:      true,
			RecursionDesired:   true,
			RecursionAvailable: true,
			Rcode:              rcode,
		},
		Question: message.Question,
	}
}

func FixedResponse(id uint16, question dns.Question, addresses []netip.Addr, timeToLive uint32) *dns.Msg {
	response := dns.Msg{
		MsgHdr: dns.MsgHdr{
			Id:                 id,
			Response:           true,
			Authoritative:      true,
			RecursionDesired:   true,
			RecursionAvailable: true,
			Rcode:              dns.RcodeSuccess,
		},
		Question: []dns.Question{question},
	}
	for _, address := range addresses {
		if address.Is4() && question.Qtype == dns.TypeA {
			response.Answer = append(response.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name:   question.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    timeToLive,
				},
				A: address.AsSlice(),
			})
		} else if address.Is6() && question.Qtype == dns.TypeAAAA {
			response.Answer = append(response.Answer, &dns.AAAA{
				Hdr: dns.RR_Header{
					Name:   question.Name,
					Rrtype: dns.TypeAAAA,
					Class:  dns.ClassINET,
					Ttl:    timeToLive,
				},
				AAAA: address.AsSlice(),
			})
		}
	}
	return &response
}

func FixedResponseCNAME(id uint16, question dns.Question, record string, timeToLive uint32) *dns.Msg {
	response := dns.Msg{
		MsgHdr: dns.MsgHdr{
			Id:                 id,
			Response:           true,
			Authoritative:      true,
			RecursionDesired:   true,
			RecursionAvailable: true,
			Rcode:              dns.RcodeSuccess,
		},
		Question: []dns.Question{question},
		Answer: []dns.RR{
			&dns.CNAME{
				Hdr: dns.RR_Header{
					Name:   question.Name,
					Rrtype: dns.TypeCNAME,
					Class:  dns.ClassINET,
					Ttl:    timeToLive,
				},
				Target: record,
			},
		},
	}
	return &response
}

func FixedResponseTXT(id uint16, question dns.Question, records []string, timeToLive uint32) *dns.Msg {
	response := dns.Msg{
		MsgHdr: dns.MsgHdr{
			Id:                 id,
			Response:           true,
			Authoritative:      true,
			RecursionDesired:   true,
			RecursionAvailable: true,
			Rcode:              dns.RcodeSuccess,
		},
		Question: []dns.Question{question},
		Answer: []dns.RR{
			&dns.TXT{
				Hdr: dns.RR_Header{
					Name:   question.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    timeToLive,
				},
				Txt: records,
			},
		},
	}
	return &response
}

func FixedResponseMX(id uint16, question dns.Question, records []*net.MX, timeToLive uint32) *dns.Msg {
	response := dns.Msg{
		MsgHdr: dns.MsgHdr{
			Id:                 id,
			Response:           true,
			Authoritative:      true,
			RecursionDesired:   true,
			RecursionAvailable: true,
			Rcode:              dns.RcodeSuccess,
		},
		Question: []dns.Question{question},
	}
	for _, record := range records {
		response.Answer = append(response.Answer, &dns.MX{
			Hdr: dns.RR_Header{
				Name:   question.Name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    timeToLive,
			},
			Preference: record.Pref,
			Mx:         record.Host,
		})
	}
	return &response
}

func addMsgStaleAnswerOpt(msg *dns.Msg) {
	opt := msg.IsEdns0()
	if opt == nil {
		opt = &dns.OPT{
			Hdr: dns.RR_Header{
				Name:   ".",
				Rrtype: dns.TypeOPT,
			},
		}
		opt.SetUDPSize(4096) // Default UDP size
		msg.Extra = append(msg.Extra, opt)
	}
	opt.Option = append(opt.Option, &dns.EDNS0_EDE{
		InfoCode: dns.ExtendedErrorCodeStaleAnswer,
	})
}

func originMsgTTL(msg *dns.Msg) uint32 {
	var originTTL uint32
	for _, recordList := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, record := range recordList {
			if originTTL == 0 || record.Header().Ttl > 0 && record.Header().Ttl < originTTL {
				originTTL = record.Header().Ttl
			}
		}
	}
	return originTTL
}

func setMsgTTL(msg *dns.Msg, ttl uint32) {
	for _, recordList := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, record := range recordList {
			record.Header().Ttl = ttl
		}
	}
}

func updateMsgTTL(msg *dns.Msg, nowTTL uint32) {
	duration := originMsgTTL(msg) - nowTTL
	for _, recordList := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, record := range recordList {
			record.Header().Ttl = record.Header().Ttl - duration
		}
	}
}
