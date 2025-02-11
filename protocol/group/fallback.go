package group

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
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
	_ adapter.Referrer                = (*Fallback)(nil)
)

type Fallback struct {
	myGroupAdapter
	outbound.Adapter
	outbound                     adapter.OutboundManager
	provider                     adapter.OutboundProviderManager
	connection                   adapter.ConnectionManager
	logger                       log.ContextLogger
	tags                         []string
	link                         string
	interval                     time.Duration
	maxDelay                     uint16
	idleTimeout                  time.Duration
	group                        *FallbackGroup
	checkAccess                  sync.Mutex
	interruptExternalConnections bool
}

func NewFallback(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.FallbackOutboundOptions) (adapter.Outbound, error) {
	outbound := &Fallback{
		myGroupAdapter: myGroupAdapter{
			ctx:             ctx,
			tags:            options.Outbounds,
			uses:            options.Providers,
			useAllProviders: options.UseAllProviders,
			types:           options.Types,
			ports:           make(map[int]bool),
			providers:       make(map[string]adapter.OutboundProvider),
		},
		Adapter:                      outbound.NewAdapter(C.TypeFallback, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		provider:                     service.FromContext[adapter.OutboundProviderManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
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
		outbounds = append(outbounds, s.outbound.DefaultDirect())
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
	group, err := NewFallbackGroup(s.ctx, s.outbound, s.logger, outbounds, s.link, s.interval, s.maxDelay, s.idleTimeout, s.interruptExternalConnections)
	if err != nil {
		return err
	}
	s.group = group
	if s.Tag() != "" {
		cacheFile := service.FromContext[adapter.CacheFile](s.ctx)
		if cacheFile != nil {
			selected := cacheFile.LoadSelected(s.Tag())
			if selected != "" {
				if detour, loaded := s.group.outboundByTag[selected]; loaded {
					s.group.selected.Store(detour)
					s.group.selectedOutboundTCP.Store(detour)
					if common.Contains(detour.Network(), N.NetworkUDP) {
						s.group.selectedOutboundUDP.Store(detour)
					}
				}
			}
		}
	}
	return nil
}

func (s *Fallback) UpdateOutbounds(tag string) error {
	if _, ok := s.providers[tag]; ok {
		outbounds, err := s.pickOutbounds()
		if err != nil {
			return E.New("update outbounds failed: ", s.Tag(), ", with reason: ", err)
		}
		s.group.outbounds = outbounds
		s.group.updateOutbounds()
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
	now := s.group.selectedOutboundTCP.Load()
	if now == nil {
		now = s.group.selectedOutboundUDP.Load()
	}
	if now != nil {
		return now.Tag()
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

func (s *Fallback) References() []string {
	group := s.group
	if group == nil {
		return nil
	}
	var references []string
	selectedOutboundTCP := group.selectedOutboundTCP.Load()
	selectedOutboundUDP := group.selectedOutboundUDP.Load()
	if selectedOutboundTCP != nil {
		references = append(references, selectedOutboundTCP.Tag())
	}
	if selectedOutboundUDP != nil && selectedOutboundUDP != selectedOutboundTCP {
		references = append(references, selectedOutboundUDP.Tag())
	}
	return references
}

func (s *Fallback) setSelected(detour adapter.Outbound) {
	if s.group.selected.Swap(detour) == detour {
		return
	}
	defer s.group.interruptGroup.Interrupt(s.group.interruptExternalConnections)
	s.group.selected.Store(detour)
	s.group.selectedOutboundTCP.Store(detour)
	if common.Contains(detour.Network(), N.NetworkUDP) {
		s.group.selectedOutboundUDP.Store(detour)
	}
	if s.Tag() == "" {
		return
	}
	cacheFile := service.FromContext[adapter.CacheFile](s.ctx)
	if cacheFile == nil {
		return
	}
	err := cacheFile.StoreSelected(s.Tag(), detour.Tag())
	if err != nil {
		s.logger.Error("store selected: ", err)
	}
}

func (s *Fallback) SelectOutbound(tag string) bool {
	detour, loaded := s.group.outboundByTag[tag]
	if !loaded {
		return false
	}
	s.setSelected(detour)
	return true
}

func (s *Fallback) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *Fallback) CheckOutbounds() {
	s.group.CheckOutbounds(s.ctx, true)
}

func (s *Fallback) PerformUpdateCheck(tag string, force bool) {
	if _, exists := s.providers[tag]; !exists && !force {
		return
	}
	s.group.performUpdateCheck()
}

func (s *Fallback) InterfaceUpdated(ctx context.Context) {
	group := s.group
	if group == nil {
		return
	}
	if group.pause.IsDevicePaused() || group.pause.IsNetworkPaused() {
		return
	}
	go func() {
		s.checkAccess.Lock()
		defer s.checkAccess.Unlock()
		if ctx.Err() != nil {
			return
		}
		group.CheckOutbounds(ctx, true)
	}()
}

func (s *Fallback) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	var outbound adapter.Outbound
	switch N.NetworkName(network) {
	case N.NetworkTCP:
		outbound = s.group.selectedOutboundTCP.Load()
	case N.NetworkUDP:
		outbound = s.group.selectedOutboundUDP.Load()
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
	outbound := s.group.selectedOutboundUDP.Load()
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

func (s *Fallback) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *Fallback) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

type FallbackGroup struct {
	ctx                          context.Context
	outbound                     adapter.OutboundManager
	pause                        pause.Manager
	pauseCallback                *list.Element[pause.Callback]
	logger                       log.Logger
	outbounds                    []adapter.Outbound
	outboundByTag                map[string]adapter.Outbound
	link                         string
	interval                     time.Duration
	maxDelay                     uint16
	idleTimeout                  time.Duration
	history                      *urltest.HistoryStorage
	checking                     atomic.Bool
	selected                     common.TypedValue[adapter.Outbound]
	selectedOutboundTCP          common.TypedValue[adapter.Outbound]
	selectedOutboundUDP          common.TypedValue[adapter.Outbound]
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      bool
	lastActive                   common.TypedValue[time.Time]
}

func NewFallbackGroup(ctx context.Context, outboundManager adapter.OutboundManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, maxDelay uint16, idleTimeout time.Duration, interruptExternalConnections bool) (*FallbackGroup, error) {
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	history := service.PtrFromContext[urltest.HistoryStorage](ctx)
	if history == nil {
		return nil, E.New("missing URL test history storage")
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
	fallbackGroup := &FallbackGroup{
		ctx:                          ctx,
		outbound:                     outboundManager,
		logger:                       logger,
		outbounds:                    outbounds,
		link:                         link,
		interval:                     interval,
		maxDelay:                     maxDelay,
		idleTimeout:                  idleTimeout,
		history:                      history,
		close:                        make(chan struct{}),
		pause:                        service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExternalConnections,
	}
	fallbackGroup.selectedOutboundTCP.Store(TCPOut)
	fallbackGroup.selectedOutboundUDP.Store(UDPOut)
	fallbackGroup.updateOutbounds()
	return fallbackGroup, nil
}

func (g *FallbackGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = true
	g.lastActive.Store(time.Now())
	go g.CheckOutbounds(g.ctx, false)
}

func (g *FallbackGroup) Touch() {
	if !g.started {
		return
	}
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker != nil {
		g.lastActive.Store(time.Now())
		return
	}
	ticker := time.NewTicker(g.interval)
	g.ticker = ticker
	g.pauseCallback = pause.RegisterTicker(g.pause, ticker, g.interval, nil)
	go g.loopCheck(ticker, g.close)
}

func (g *FallbackGroup) Close() error {
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker == nil {
		return nil
	}
	g.ticker.Stop()
	g.ticker = nil
	g.pause.UnregisterCallback(g.pauseCallback)
	g.pauseCallback = nil
	close(g.close)
	return nil
}

func (g *FallbackGroup) Select(network string) (adapter.Outbound, bool) {
	minOutbound := g.selected.Load()
	if minOutbound != nil && common.Contains(minOutbound.Network(), network) {
		if history := g.history.LoadURLTestHistory(RealTag(g.outbound, minOutbound)); history != nil {
			if g.maxDelay == 0 || (g.maxDelay > 0 && history.Delay < g.maxDelay) {
				return minOutbound, true
			}
		}
	}
	var minDelay uint16
	var fallbackIgnoreOutboundDelay uint16
	var fallbackIgnoreOutbound adapter.Outbound
	switch network {
	case N.NetworkTCP:
		minOutbound = g.selectedOutboundTCP.Load()
	case N.NetworkUDP:
		minOutbound = g.selectedOutboundUDP.Load()
	}
	if minOutbound != nil {
		if history := g.history.LoadURLTestHistory(RealTag(g.outbound, minOutbound)); history != nil {
			minDelay = history.Delay
		} else {
			minOutbound = nil
		}
	}
	for _, detour := range g.outbounds {
		if !common.Contains(detour.Network(), network) {
			continue
		}
		history := g.history.LoadURLTestHistory(RealTag(g.outbound, detour))
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

func (g *FallbackGroup) loopCheck(ticker *time.Ticker, closeChan <-chan struct{}) {
	if time.Since(g.lastActive.Load()) > g.interval {
		g.lastActive.Store(time.Now())
		g.CheckOutbounds(g.ctx, false)
	}
	for {
		select {
		case <-closeChan:
			return
		case <-ticker.C:
		}
		if time.Since(g.lastActive.Load()) > g.idleTimeout {
			g.access.Lock()
			if g.ticker == ticker {
				g.ticker.Stop()
				g.ticker = nil
				g.pause.UnregisterCallback(g.pauseCallback)
				g.pauseCallback = nil
			}
			g.access.Unlock()
			return
		}
		g.CheckOutbounds(g.ctx, false)
	}
}

func (g *FallbackGroup) CheckOutbounds(ctx context.Context, force bool) {
	_, _ = g.urlTest(ctx, force)
}

func (g *FallbackGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(ctx, true)
}

func (g *FallbackGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
	if g.checking.Swap(true) {
		return make(map[string]uint16), nil
	}
	defer g.checking.Store(false)
	result := URLTestOutbounds(ctx, g.outbound, g.history, g.logger, g.outbounds, g.link, g.interval, force)
	g.performUpdateCheck()
	return result, nil
}

func (g *FallbackGroup) performUpdateCheck() {
	var updated bool
	selected := g.selectedOutboundTCP.Load()
	if outbound, exists := g.Select(N.NetworkTCP); outbound != nil && (selected == nil || (exists && outbound != selected)) {
		if selected != nil {
			updated = true
		}
		g.selectedOutboundTCP.Store(outbound)
	}
	selected = g.selectedOutboundUDP.Load()
	if outbound, exists := g.Select(N.NetworkUDP); outbound != nil && (selected == nil || (exists && outbound != selected)) {
		if selected != nil {
			updated = true
		}
		g.selectedOutboundUDP.Store(outbound)
	}
	if updated {
		g.interruptGroup.Interrupt(g.interruptExternalConnections)
	}
}

func (g *FallbackGroup) updateOutbounds() {
	g.outboundByTag = make(map[string]adapter.Outbound)
	for _, outbound := range g.outbounds {
		g.outboundByTag[outbound.Tag()] = outbound
	}
	if selected := g.selected.Load(); selected != nil {
		if _, exists := g.outboundByTag[selected.Tag()]; !exists {
			g.selected.Store(nil)
		}
	}
	if selected := g.selectedOutboundTCP.Load(); selected != nil {
		if _, exists := g.outboundByTag[selected.Tag()]; !exists {
			g.selectedOutboundTCP.Store(nil)
		}
	}
	if selected := g.selectedOutboundUDP.Load(); selected != nil {
		if _, exists := g.outboundByTag[selected.Tag()]; !exists {
			g.selectedOutboundUDP.Store(nil)
		}
	}
}
