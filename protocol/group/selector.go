package group

import (
	"context"
	"net"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	R "github.com/dlclark/regexp2"
)

func RegisterSelector(registry *outbound.Registry) {
	outbound.Register[option.SelectorOutboundOptions](registry, C.TypeSelector, NewSelector)
}

var (
	_ adapter.OutboundGroup             = (*Selector)(nil)
	_ adapter.SelectorGroup             = (*Selector)(nil)
	_ adapter.ConnectionHandlerEx       = (*Selector)(nil)
	_ adapter.PacketConnectionHandlerEx = (*Selector)(nil)
)

type Selector struct {
	myGroupAdapter
	outbound.Adapter
	outbound                     adapter.OutboundManager
	provider                     adapter.OutboundProviderManager
	connection                   adapter.ConnectionManager
	logger                       logger.ContextLogger
	defaultTag                   string
	outbounds                    []adapter.Outbound
	outboundByTag                map[string]adapter.Outbound
	selected                     common.TypedValue[adapter.Outbound]
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
}

func NewSelector(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.SelectorOutboundOptions) (adapter.Outbound, error) {
	outbound := &Selector{
		myGroupAdapter: myGroupAdapter{
			ctx:             ctx,
			tags:            options.Outbounds,
			uses:            options.Providers,
			useAllProviders: options.UseAllProviders,
			types:           options.Types,
			ports:           make(map[int]bool),
			providers:       make(map[string]adapter.OutboundProvider),
		},
		Adapter:                      outbound.NewAdapter(C.TypeSelector, tag, nil, options.Outbounds),
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		provider:                     service.FromContext[adapter.OutboundProviderManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		defaultTag:                   options.Default,
		outbounds:                    []adapter.Outbound{},
		outboundByTag:                make(map[string]adapter.Outbound),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: options.InterruptExistConnections,
	}
	if len(outbound.tags) == 0 && len(outbound.uses) == 0 && !outbound.useAllProviders {
		return nil, E.New("missing tags and uses")
	}
	if len(options.Includes) > 0 {
		includes := make([]*R.Regexp, 0, len(options.Includes))
		for i, include := range options.Includes {
			regex, err := R.Compile(include, R.IgnoreCase)
			if err != nil {
				return nil, E.Cause(err, "parse includes[", i, "]")
			}
			includes = append(includes, regex)
		}
		outbound.includes = includes
	}
	if options.Excludes != "" {
		regex, err := R.Compile(options.Excludes, R.IgnoreCase)
		if err != nil {
			return nil, E.Cause(err, "parse excludes")
		}
		outbound.excludes = regex
	}
	if !CheckType(outbound.types) {
		return nil, E.New("invalid types")
	}
	if portMap, err := CreatePortsMap(options.Ports); err == nil {
		outbound.ports = portMap
	} else {
		return nil, err
	}
	return outbound, nil
}

func (s *Selector) Network() []string {
	selected := s.selected.Load()
	if selected == nil {
		return []string{N.NetworkTCP, N.NetworkUDP}
	}
	return selected.Network()
}

func (s *Selector) Start() error {
	if s.useAllProviders {
		uses := []string{}
		for _, provider := range s.provider.OutboundProviders() {
			uses = append(uses, provider.Tag())
		}
		s.uses = uses
	}
	outbounds, outboundByTag, err := s.pickOutbounds()
	s.outbounds = outbounds
	s.outboundByTag = outboundByTag
	return err
}

func (s *Selector) pickOutbounds() ([]adapter.Outbound, map[string]adapter.Outbound, error) {
	outbounds := []adapter.Outbound{}
	outboundByTag := map[string]adapter.Outbound{}

	for i, tag := range s.tags {
		detour, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return nil, nil, E.New("outbound ", i, " not found: ", tag)
		}
		outbounds = append(outbounds, detour)
		outboundByTag[tag] = detour
	}

	for i, tag := range s.uses {
		provider, loaded := s.provider.OutboundProvider(tag)
		if !loaded {
			return nil, nil, E.New("outbound provider ", i, " not found: ", tag)
		}
		if _, ok := s.providers[tag]; !ok {
			s.providers[tag] = provider
		}
		for _, outbound := range provider.Outbounds() {
			if !s.OutboundFilter(outbound) {
				continue
			}
			tag := outbound.Tag()
			outbounds = append(outbounds, outbound)
			outboundByTag[tag] = outbound
		}
	}

	if len(outbounds) == 0 {
		defaultFallback := s.outbound.DefaultDirect()
		outbounds = append(outbounds, defaultFallback)
		outboundByTag[defaultFallback.Tag()] = defaultFallback
		s.selected.Store(defaultFallback)
		return outbounds, outboundByTag, nil
	}

	if s.Tag() != "" {
		cacheFile := service.FromContext[adapter.CacheFile](s.ctx)
		if cacheFile != nil {
			selected := cacheFile.LoadSelected(s.Tag())
			if selected != "" {
				detour, loaded := outboundByTag[selected]
				if loaded {
					s.selected.Store(detour)
					return outbounds, outboundByTag, nil
				}
			}
		}
	}

	if s.defaultTag != "" {
		detour, loaded := outboundByTag[s.defaultTag]
		if !loaded {
			return nil, nil, E.New("default outbound not found: ", s.defaultTag)
		}
		s.selected.Store(detour)
		return outbounds, outboundByTag, nil
	}

	s.selected.Store(outbounds[0])
	return outbounds, outboundByTag, nil
}

func (s *Selector) UpdateOutbounds(tag string) error {
	if _, ok := s.providers[tag]; ok {
		outbounds, outboundByTag, err := s.pickOutbounds()
		if err != nil {
			return E.New("update oubounds failed: ", s.Tag())
		}
		s.outbounds = outbounds
		s.outboundByTag = outboundByTag
	}
	return nil
}

func (s *Selector) Now() string {
	selected := s.selected.Load()
	if selected == nil {
		return s.tags[0]
	}
	return selected.Tag()
}

func (s *Selector) All() []string {
	var all []string
	for _, outbound := range s.outbounds {
		all = append(all, outbound.Tag())
	}
	return all
}

func (s *Selector) SelectOutbound(tag string) bool {
	detour, loaded := s.outboundByTag[tag]
	if !loaded {
		return false
	}
	if s.selected.Swap(detour) == detour {
		return true
	}
	if s.Tag() != "" {
		cacheFile := service.FromContext[adapter.CacheFile](s.ctx)
		if cacheFile != nil {
			err := cacheFile.StoreSelected(s.Tag(), detour.Tag())
			if err != nil {
				s.logger.Error("store selected: ", err)
			}
		}
	}
	s.interruptGroup.Interrupt(s.interruptExternalConnections)
	return true
}

func (s *Selector) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	conn, err := s.selected.Load().DialContext(ctx, network, destination)
	if err != nil {
		return nil, err
	}
	return s.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Selector) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	conn, err := s.selected.Load().ListenPacket(ctx, destination)
	if err != nil {
		return nil, err
	}
	return s.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
}

func (s *Selector) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	selected := s.selected.Load()
	if outboundHandler, isHandler := selected.(adapter.ConnectionHandlerEx); isHandler {
		outboundHandler.NewConnectionEx(ctx, conn, metadata, onClose)
	} else {
		s.connection.NewConnection(ctx, selected, conn, metadata, onClose)
	}
}

func (s *Selector) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	selected := s.selected.Load()
	if outboundHandler, isHandler := selected.(adapter.PacketConnectionHandlerEx); isHandler {
		outboundHandler.NewPacketConnectionEx(ctx, conn, metadata, onClose)
	} else {
		s.connection.NewPacketConnection(ctx, selected, conn, metadata, onClose)
	}
}

func (s *Selector) NewDirectRouteConnection(metadata adapter.InboundContext, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error) {
	selected := s.selected.Load()
	if !common.Contains(selected.Network(), metadata.Network) {
		return nil, E.New(metadata.Network, " is not supported by outbound: ", selected.Tag())
	}
	return selected.(adapter.DirectRouteOutbound).NewDirectRouteConnection(metadata, routeContext, timeout)
}

func RealTag(detour adapter.Outbound) string {
	if group, isGroup := detour.(adapter.OutboundGroup); isGroup {
		return group.Now()
	}
	return detour.Tag()
}
