package group

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/atomic"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"

	R "github.com/dlclark/regexp2"
)

func RegisterFallback(registry *outbound.Registry) {
	outbound.Register[option.FallbackOutboundOptions](registry, C.TypeFallback, NewFallback)
}

var (
	_ adapter.OutboundGroup           = (*Fallback)(nil)
	_ adapter.InterfaceUpdateListener = (*Fallback)(nil)
)

type Fallback struct {
	outbound.Adapter
	router                       adapter.Router
	outbound                     adapter.OutboundManager
	provider                     adapter.OutboundProviderManager
	connection                   adapter.ConnectionManager
	logger                       log.ContextLogger
	myGroupAdapter
	tags                         []string
	link                         string
	interval                     time.Duration
	maxDelay                     uint16
	idleTimeout                  time.Duration
	group                        *FallbackGroup
	interruptExternalConnections bool
}

func NewFallback(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.FallbackOutboundOptions) (adapter.Outbound, error) {
	outbound := &Fallback{
		Adapter:                      outbound.NewAdapter(C.TypeFallback, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		router:                       router,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		provider:                     service.FromContext[adapter.OutboundProviderManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		myGroupAdapter: myGroupAdapter{
			ctx:             ctx,
			tags:            options.Outbounds,
			uses:            options.Providers,
			useAllProviders: options.UseAllProviders,
			types:           options.Types,
			ports:           make(map[int]bool),
			providers:       make(map[string]adapter.OutboundProvider),
		},
		tags:                         options.Outbounds,
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		maxDelay:                     uint16(time.Duration(options.MaxDelay).Milliseconds()),
		idleTimeout:                  time.Duration(options.IdleTimeout),
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

func (s *Fallback) pickOutbounds() ([]adapter.Outbound, error) {
	outbounds := []adapter.Outbound{}
	for i, tag := range s.tags {
		detour, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return nil, E.New("outbound ", i, " not found: ", tag)
		}
		outbounds = append(outbounds, detour)
	}
	for i, tag := range s.uses {
		provider, loaded := s.provider.OutboundProvider(tag)
		if !loaded {
			return nil, E.New("provider ", i, " not found: ", tag)
		}
		if _, ok := s.providers[tag]; !ok {
			s.providers[tag] = provider
		}
		for _, outbound := range provider.Outbounds() {
			if !s.OutboundFilter(outbound) {
				continue
			}
			outbounds = append(outbounds, outbound)
		}
	}
	if len(outbounds) == 0 {
		OUTBOUNDLESS, _ := s.outbound.Outbound("OUTBOUNDLESS")
		outbounds = append(outbounds, OUTBOUNDLESS)
	}
	return outbounds, nil
}

func (s *Fallback) Start() error {
	if s.useAllProviders {
		uses := []string{}
		for _, provider := range s.provider.OutboundProviders() {
			uses = append(uses, provider.Tag())
		}
		s.uses = uses
	}
	outbounds, err := s.pickOutbounds()
	if err != nil {
		return err
	}
	group, err := NewFallbackGroup(s.ctx, s.outbound, s.provider, s.logger, outbounds, s.link, s.interval, s.maxDelay, s.idleTimeout, s.interruptExternalConnections)
	if err != nil {
		return err
	}
	s.group = group
	return nil
}

func (s *Fallback) UpdateOutbounds(tag string) error {
	if _, ok := s.providers[tag]; ok {
		outbounds, err := s.pickOutbounds()
		if err != nil {
			return E.New("update outbounds failed: ", s.Tag(), ", with reason: ", err)
		}
		s.group.outbounds = outbounds
		s.group.performUpdateCheck()
	}
	return nil
}

func (s *Fallback) PostStart() error {
	s.group.PostStart()
	return nil
}

func (s *Fallback) Close() error {
	return common.Close(
		common.PtrOrNil(s.group),
	)
}

func (s *Fallback) Now() string {
	if s.group.selectedOutboundTCP != nil {
		return s.group.selectedOutboundTCP.Tag()
	} else if s.group.selectedOutboundUDP != nil {
		return s.group.selectedOutboundUDP.Tag()
	}
	return ""
}

func (s *Fallback) All() []string {
	var all []string
	for _, outbound := range s.group.outbounds {
		all = append(all, outbound.Tag())
	}
	return all
}

func (s *Fallback) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *Fallback) CheckOutbounds() {
	s.group.CheckOutbounds(true)
}

func (s *Fallback) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	var outbound adapter.Outbound
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		outbound = s.group.selectedOutboundTCP
	case N.NetworkUDP:
		outbound = s.group.selectedOutboundUDP
	default:
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	if outbound == nil {
		outbound, _ = s.group.Select(network)
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := outbound.DialContext(ctx, network, destination)
	if err == nil {
		return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.group.history.DeleteURLTestHistory(outbound.Tag())
	return nil, err
}

func (s *Fallback) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	outbound := s.group.selectedOutboundUDP
	if outbound == nil {
		outbound, _ = s.group.Select(N.NetworkUDP)
	}
	if outbound == nil {
		return nil, E.New("missing supported outbound")
	}
	conn, err := outbound.ListenPacket(ctx, destination)
	if err == nil {
		return s.group.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.group.history.DeleteURLTestHistory(outbound.Tag())
	return nil, err
}

func (s *Fallback) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *Fallback) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

func (s *Fallback) InterfaceUpdated() {
	go s.group.CheckOutbounds(true)
	return
}

func (s *Fallback) PerformUpdateCheck(tag string, force bool) {
	if _, exists := s.providers[tag]; !exists && !force {
		return
	}
	s.group.performUpdateCheck()
}

type FallbackGroup struct {
	ctx                          context.Context
	router                       adapter.Router
	outboundManager              adapter.OutboundManager
	providerManager              adapter.OutboundProviderManager
	logger                       log.Logger
	outbounds                    []adapter.Outbound
	link                         string
	interval                     time.Duration
	maxDelay                     uint16
	idleTimeout                  time.Duration
	history                      adapter.URLTestHistoryStorage
	checking                     atomic.Bool
	pauseManager                 pause.Manager
	selectedOutboundTCP          adapter.Outbound
	selectedOutboundUDP          adapter.Outbound
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool

	access     sync.Mutex
	ticker     *time.Ticker
	close      chan struct{}
	started    bool
	lastActive atomic.TypedValue[time.Time]
}

func NewFallbackGroup(ctx context.Context, outboundManager adapter.OutboundManager, providerManager adapter.OutboundProviderManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, maxDelay uint16, idleTimeout time.Duration, interruptExternalConnections bool) (*FallbackGroup, error) {
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	var history adapter.URLTestHistoryStorage
	if historyFromCtx := service.PtrFromContext[urltest.HistoryStorage](ctx); historyFromCtx != nil {
		history = historyFromCtx
	} else if clashServer := service.FromContext[adapter.ClashServer](ctx); clashServer != nil {
		history = clashServer.HistoryStorage()
	} else {
		history = urltest.NewHistoryStorage()
	}
	var TCPOut, UDPOut adapter.Outbound
	for _, detour := range outbounds {
		if TCPOut == nil && common.Contains(detour.Network(), N.NetworkTCP) {
			TCPOut = detour
		}
		if UDPOut == nil && common.Contains(detour.Network(), N.NetworkUDP) {
			UDPOut = detour
		}
		if TCPOut != nil && UDPOut != nil {
			break
		}
	}
	return &FallbackGroup{
		ctx:                          ctx,
		outboundManager:              outboundManager,
		providerManager:              providerManager,
		logger:                       logger,
		outbounds:                    outbounds,
		link:                         link,
		interval:                     interval,
		maxDelay:                     maxDelay,
		idleTimeout:                  idleTimeout,
		history:                      history,
		close:                        make(chan struct{}),
		pauseManager:                 service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		selectedOutboundTCP:          TCPOut,
		selectedOutboundUDP:          UDPOut,
		interruptExternalConnections: interruptExternalConnections,
	}, nil
}

func (g *FallbackGroup) PostStart() {
	g.started = true
	g.lastActive.Store(time.Now())
	go g.CheckOutbounds(false)
}

func (g *FallbackGroup) Touch() {
	if !g.started {
		return
	}
	if g.ticker != nil {
		g.lastActive.Store(time.Now())
		return
	}
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker != nil {
		return
	}
	g.ticker = time.NewTicker(g.interval)
	go g.loopCheck()
}

func (g *FallbackGroup) Close() error {
	if g.ticker == nil {
		return nil
	}
	g.ticker.Stop()
	close(g.close)
	return nil
}

func (g *FallbackGroup) Select(network string) (adapter.Outbound, bool) {
	var minDelay uint16
	var minOutbound adapter.Outbound
	var fallbackIgnoreOutboundDelay uint16
	var fallbackIgnoreOutbound adapter.Outbound
	switch network {
	case N.NetworkTCP:
		if g.selectedOutboundTCP != nil {
			if history := g.history.LoadURLTestHistory(RealTag(g.selectedOutboundTCP)); history != nil {
				minOutbound = g.selectedOutboundTCP
				minDelay = history.Delay
			}
		}
	case N.NetworkUDP:
		if g.selectedOutboundUDP != nil {
			if history := g.history.LoadURLTestHistory(RealTag(g.selectedOutboundUDP)); history != nil {
				minOutbound = g.selectedOutboundUDP
				minDelay = history.Delay
			}
		}
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(detour))
		if history == nil {
			continue
		}
		if g.maxDelay > 0 && history.Delay > g.maxDelay {
			if fallbackIgnoreOutboundDelay == 0 || history.Delay < fallbackIgnoreOutboundDelay {
				fallbackIgnoreOutboundDelay = history.Delay
				fallbackIgnoreOutbound = detour
			}
			continue
		}
		minDelay = history.Delay
		minOutbound = detour
		break
	}
	if fallbackIgnoreOutbound != nil && fallbackIgnoreOutboundDelay < minDelay {
		minOutbound = fallbackIgnoreOutbound
	}
	if minOutbound == nil && fallbackIgnoreOutbound != nil {
		return fallbackIgnoreOutbound, true
	}
	if minOutbound == nil {
		for _, detour := range g.outbounds {
			if !common.Contains(detour.Network(), network) {
				continue
			}
			return detour, false
		}
		return nil, false
	}
	return minOutbound, true
}

func (g *FallbackGroup) loopCheck() {
	if time.Now().Sub(g.lastActive.Load()) > g.interval {
		g.lastActive.Store(time.Now())
		g.CheckOutbounds(false)
	}
	for {
		select {
		case <-g.close:
			return
		case <-g.ticker.C:
		}
		if time.Now().Sub(g.lastActive.Load()) > g.idleTimeout {
			g.access.Lock()
			g.ticker.Stop()
			g.ticker = nil
			g.access.Unlock()
			return
		}
		g.pauseManager.WaitActive()
		g.CheckOutbounds(false)
	}
}

func (g *FallbackGroup) CheckOutbounds(force bool) {
	_, _ = g.urlTest(g.ctx, force)
}

func (g *FallbackGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(ctx, false)
}

func (g *FallbackGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	result := make(map[string]uint16)
	if g.checking.Swap(true) {
		return result, nil
	}
	defer g.checking.Store(false)
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](10))
	checked := make(map[string]bool)
	var resultAccess sync.Mutex
	for _, detour := range g.outbounds {
		tag := detour.Tag()
		realTag := RealTag(detour)
		if checked[realTag] {
			continue
		}
		history := g.history.LoadURLTestHistory(realTag)
		if !force && history != nil && time.Now().Sub(history.Time) < g.interval {
			continue
		}
		checked[realTag] = true
		p, loaded := g.providerManager.OutboundWithProvider(realTag)
		if !loaded {
			continue
		}
		b.Go(realTag, func() (any, error) {
			testCtx, cancel := context.WithTimeout(g.ctx, C.TCPTimeout)
			defer cancel()
			t, err := urltest.URLTest(testCtx, g.link, p)
			if err != nil {
				g.logger.Debug("outbound ", tag, " unavailable: ", err)
				g.history.DeleteURLTestHistory(realTag)
			} else {
				g.logger.Debug("outbound ", tag, " available: ", t, "ms")
				g.history.StoreURLTestHistory(realTag, &adapter.URLTestHistory{
					Time:  time.Now(),
					Delay: t,
				})
				resultAccess.Lock()
				result[tag] = t
				resultAccess.Unlock()
			}
			return nil, nil
		})
	}
	b.Wait()
	g.performUpdateCheck()
	return result, nil
}

func (g *FallbackGroup) performUpdateCheck() {
	var updated bool
	if outbound, exists := g.Select(N.NetworkTCP); outbound != nil && (g.selectedOutboundTCP == nil || (exists && outbound != g.selectedOutboundTCP)) {
		g.selectedOutboundTCP = outbound
		updated = true
	}
	if outbound, exists := g.Select(N.NetworkUDP); outbound != nil && (g.selectedOutboundUDP == nil || (exists && outbound != g.selectedOutboundUDP)) {
		g.selectedOutboundUDP = outbound
		updated = true
	}
	if updated {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
}
