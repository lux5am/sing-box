package transport

import (
	"context"
	"sync"

	"github.com/sagernet/sing-box/adapter"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

var _ adapter.DNSTransport = (*GroupTransport)(nil)

func RegisterGroup(registry *dns.TransportRegistry) {
	dns.RegisterTransport[option.GroupDNSServerOptions](registry, C.DNSTypeGroup, NewGroup)
}

type GroupTransport struct {
	dns.TransportAdapter

	ctx        context.Context
	logger     log.ContextLogger
	serverTags []string
	transports []adapter.DNSTransport
}

func NewGroup(ctx context.Context, logger log.ContextLogger, tag string, options option.GroupDNSServerOptions) (adapter.DNSTransport, error) {
	if len(options.Servers) == 0 {
		return nil, E.New("missing servers")
	}
	return &GroupTransport{
		TransportAdapter: dns.NewTransportAdapter(C.DNSTypeGroup, tag, options.Servers),
		ctx:              ctx,
		logger:           logger,
		serverTags:       options.Servers,
	}, nil
}

func (t *GroupTransport) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	transportManager := service.FromContext[adapter.DNSTransportManager](t.ctx)
	if transportManager == nil {
		return E.New("missing DNS transport manager")
	}
	for _, tag := range t.serverTags {
		transport, loaded := transportManager.Transport(tag)
		if !loaded {
			return E.New("DNS server not found: ", tag)
		}
		if transport.Type() == C.DNSTypeGroup {
			return E.New("group cannot contain another group: ", tag)
		}
		if transport.Type() == C.DNSTypeFakeIP {
			return E.New("group cannot contain fakeip server: ", tag)
		}
		t.transports = append(t.transports, transport)
	}
	return nil
}

func (t *GroupTransport) Close() error {
	return nil
}

func (t *GroupTransport) Reset() {
}

func (t *GroupTransport) Exchange(ctx context.Context, message *mDNS.Msg) (*mDNS.Msg, error) {
	if len(t.transports) == 1 {
		return t.transports[0].Exchange(ctx, message)
	}
	raceCtx, raceCancel := context.WithCancel(ctx)
	defer raceCancel()
	type result struct {
		msg *mDNS.Msg
		tag string
	}
	resultChan := make(chan result, 1)
	var firstError error
	var once sync.Once
	for _, transport := range t.transports {
		go func(transport adapter.DNSTransport) {
			resp, err := transport.Exchange(raceCtx, message.Copy())
			if err != nil {
				once.Do(func() {
					firstError = err
				})
				return
			}
			if resp != nil {
				select {
				case resultChan <- result{msg: resp, tag: transport.Tag()}:
				default:
				}
			}
		}(transport)
	}
	select {
	case resp := <-resultChan:
		t.logger.DebugContext(raceCtx, "fastest response from ", resp.tag)
		return resp.msg, nil
	case <-raceCtx.Done():
		if firstError != nil {
			return nil, E.New("all DNS requests failed, first error: ", firstError)
		}
		return nil, E.New("all DNS requests failed")
	}
}
