package group

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/interrupt"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/atomic"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/contrab/freelru"
	"github.com/sagernet/sing/contrab/maphash"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/pause"

	R "github.com/dlclark/regexp2"
	"golang.org/x/net/publicsuffix"
)

func RegisterLoadBalance(registry *outbound.Registry) {
	outbound.Register[option.LoadBalanceOutboundOptions](registry, C.TypeLoadBalance, NewLoadBalance)
}

var _ adapter.OutboundGroup = (*LoadBalance)(nil)

const (
	StrategyRoundRobin        = "round-robin"
	StrategyConsistentHashing = "consistent-hashing"
	StrategyStickySessions    = "sticky-sessions"
)

type LoadBalance struct {
	myGroupAdapter
	outbound.Adapter
	router                       adapter.Router
	outbound                     adapter.OutboundManager
	provider                     adapter.OutboundProviderManager
	connection                   adapter.ConnectionManager
	logger                       log.ContextLogger
	tags                         []string
	link                         string
	interval                     time.Duration
	idleTimeout                  time.Duration
	ttl                          time.Duration
	group                        *LoadBalanceGroup
	interruptExternalConnections bool
	strategy                     string
}

func NewLoadBalance(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.LoadBalanceOutboundOptions) (adapter.Outbound, error) {
	strategy := options.Strategy
	if strategy == "" {
		strategy = StrategyRoundRobin
	}
	switch strategy {
	case StrategyRoundRobin, StrategyConsistentHashing, StrategyStickySessions:
	default:
		return nil, E.New("load-balance strategy not found: ", strategy)
	}
	outbound := &LoadBalance{
		myGroupAdapter: myGroupAdapter{
			ctx:             ctx,
			tags:            options.Outbounds,
			uses:            options.Providers,
			useAllProviders: options.UseAllProviders,
			types:           options.Types,
			ports:           make(map[int]bool),
			providers:       make(map[string]adapter.OutboundProvider),
		},
		Adapter:                      outbound.NewAdapter(C.TypeLoadBalance, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		router:                       router,
		outbound:                     service.FromContext[adapter.OutboundManager](ctx),
		provider:                     service.FromContext[adapter.OutboundProviderManager](ctx),
		connection:                   service.FromContext[adapter.ConnectionManager](ctx),
		logger:                       logger,
		tags:                         options.Outbounds,
		link:                         options.URL,
		interval:                     time.Duration(options.Interval),
		ttl:                          time.Duration(options.TTL),
		idleTimeout:                  time.Duration(options.IdleTimeout),
		interruptExternalConnections: options.InterruptExistConnections,
		strategy:                     strategy,
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

func (s *LoadBalance) pickOutbounds() ([]adapter.Outbound, error) {
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

func (s *LoadBalance) Start() error {
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
	group, err := NewLoadBalanceGroup(s.ctx, s.outbound, s.provider, s.logger, outbounds, s.link, s.interval, s.idleTimeout, s.ttl, s.interruptExternalConnections, s.strategy)
	if err != nil {
		return err
	}
	s.group = group
	return nil
}

func (s *LoadBalance) UpdateOutbounds(tag string) error {
	if _, ok := s.providers[tag]; ok {
		outbounds, err := s.pickOutbounds()
		if err != nil {
			return E.New("update outbounds failed: ", s.Tag(), ", with reason: ", err)
		}
		s.group.outbounds = outbounds
	}
	return nil
}

func (s *LoadBalance) PostStart() error {
	s.group.PostStart()
	return nil
}

func (s *LoadBalance) Close() error {
	return common.Close(
		common.PtrOrNil(s.group),
	)
}

func (s *LoadBalance) Now() string {
	return ""
}

func (s *LoadBalance) All() []string {
	var all []string
	for _, outbound := range s.group.outbounds {
		all = append(all, outbound.Tag())
	}
	return all
}

func (s *LoadBalance) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.group.URLTest(ctx)
}

func (s *LoadBalance) CheckOutbounds() {
	s.group.CheckOutbounds(true)
}

func (s *LoadBalance) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	s.group.Touch()
	metadata := adapter.ContextFrom(ctx)
	outbound := s.group.Unwrap(metadata, true)
	if outbound == nil || !common.Contains(outbound.Network(), network) {
		return nil, E.New("missing supported outbound")
	}
	if metadata != nil {
		metadata.SetRealOutbound(outbound.Tag())
	}
	conn, err := outbound.DialContext(ctx, network, destination)
	if err == nil {
		return s.group.interruptGroup.NewConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.group.history.DeleteURLTestHistory(outbound.Tag())
	return nil, err
}

func (s *LoadBalance) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	s.group.Touch()
	metadata := adapter.ContextFrom(ctx)
	outbound := s.group.Unwrap(metadata, true)
	if outbound == nil || !common.Contains(outbound.Network(), N.NetworkUDP) {
		return nil, E.New("missing supported outbound")
	}
	if metadata != nil {
		metadata.SetRealOutbound(outbound.Tag())
	}
	conn, err := outbound.ListenPacket(ctx, destination)
	if err == nil {
		return s.group.interruptGroup.NewPacketConn(conn, interrupt.IsExternalConnectionFromContext(ctx)), nil
	}
	s.logger.ErrorContext(ctx, err)
	s.group.history.DeleteURLTestHistory(outbound.Tag())
	return nil, err
}

func (s *LoadBalance) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *LoadBalance) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	ctx = interrupt.ContextWithIsExternalConnection(ctx)
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

func (s *LoadBalance) NewDirectRouteConnection(metadata adapter.InboundContext, routeContext tun.DirectRouteContext, timeout time.Duration) (tun.DirectRouteDestination, error) {
	s.group.Touch()
	selected := s.group.Unwrap(&metadata, true)
	if selected == nil {
		return nil, E.New("missing supported outbound")
	}
	if !common.Contains(selected.Network(), metadata.Network) {
		return nil, E.New(metadata.Network, " is not supported by outbound: ", selected.Tag())
	}
	return selected.(adapter.DirectRouteOutbound).NewDirectRouteConnection(metadata, routeContext, timeout)
}

type strategyFn = func(metadata *adapter.InboundContext, touch bool) adapter.Outbound

type LoadBalanceGroup struct {
	ctx                          context.Context
	router                       adapter.Router
	provider                     adapter.OutboundProviderManager
	pause                        pause.Manager
	pauseCallback                *list.Element[pause.Callback]
	logger                       log.Logger
	outbounds                    []adapter.Outbound
	link                         string
	interval                     time.Duration
	idleTimeout                  time.Duration
	ttl                          time.Duration
	history                      adapter.URLTestHistoryStorage
	checking                     atomic.Bool
	interruptGroup               *interrupt.Group
	interruptExternalConnections bool
	access                       sync.Mutex
	ticker                       *time.Ticker
	close                        chan struct{}
	started                      bool
	lastActive                   atomic.TypedValue[time.Time]
	strategyFn                   strategyFn
}

func NewLoadBalanceGroup(ctx context.Context, outboundManager adapter.OutboundManager, providerManager adapter.OutboundProviderManager, logger log.Logger, outbounds []adapter.Outbound, link string, interval time.Duration, idleTimeout time.Duration, ttl time.Duration, interruptExternalConnections bool, strategy string) (*LoadBalanceGroup, error) {
	if interval == 0 {
		interval = C.DefaultURLTestInterval
	}
	if idleTimeout == 0 {
		idleTimeout = C.DefaultURLTestIdleTimeout
	}
	if interval > idleTimeout {
		return nil, E.New("interval must be less or equal than idle_timeout")
	}
	if ttl == 0 {
		ttl = time.Minute * 10
	}
	var history adapter.URLTestHistoryStorage
	if historyFromCtx := service.PtrFromContext[urltest.HistoryStorage](ctx); historyFromCtx != nil {
		history = historyFromCtx
	} else if clashServer := service.FromContext[adapter.ClashServer](ctx); clashServer != nil {
		history = clashServer.HistoryStorage()
	} else {
		history = urltest.NewHistoryStorage()
	}
	if link == "" {
		link = "https://www.gstatic.com/generate_204"
	}
	loadBalanceGroup := &LoadBalanceGroup{
		ctx:                          ctx,
		provider:                     providerManager,
		logger:                       logger,
		outbounds:                    outbounds,
		link:                         link,
		interval:                     interval,
		idleTimeout:                  idleTimeout,
		ttl:                          ttl,
		history:                      history,
		close:                        make(chan struct{}),
		pause:                        service.FromContext[pause.Manager](ctx),
		interruptGroup:               interrupt.NewGroup(),
		interruptExternalConnections: interruptExternalConnections,
	}
	switch strategy {
	case StrategyRoundRobin:
		loadBalanceGroup.strategyFn = strategyRoundRobin(loadBalanceGroup, link)
	case StrategyConsistentHashing:
		loadBalanceGroup.strategyFn = strategyConsistentHashing(loadBalanceGroup, link)
	case StrategyStickySessions:
		loadBalanceGroup.strategyFn = strategyStickySessions(loadBalanceGroup, link)
	}
	return loadBalanceGroup, nil
}

func (g *LoadBalanceGroup) PostStart() {
	g.access.Lock()
	defer g.access.Unlock()
	g.started = true
	g.lastActive.Store(time.Now())
	go g.CheckOutbounds(false)
}

func (g *LoadBalanceGroup) Touch() {
	if !g.started {
		return
	}
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker != nil {
		g.lastActive.Store(time.Now())
		return
	}
	g.ticker = time.NewTicker(g.interval)
	go g.loopCheck()
	g.pauseCallback = pause.RegisterTicker(g.pause, g.ticker, g.interval, nil)
}

func (g *LoadBalanceGroup) Close() error {
	g.access.Lock()
	defer g.access.Unlock()
	if g.ticker == nil {
		return nil
	}
	g.ticker.Stop()
	g.pause.UnregisterCallback(g.pauseCallback)
	close(g.close)
	return nil
}

func (g *LoadBalanceGroup) loopCheck() {
	if time.Since(g.lastActive.Load()) > g.interval {
		g.lastActive.Store(time.Now())
		g.CheckOutbounds(false)
	}
	for {
		select {
		case <-g.close:
			return
		case <-g.ticker.C:
		}
		if time.Since(g.lastActive.Load()) > g.idleTimeout {
			g.access.Lock()
			g.ticker.Stop()
			g.ticker = nil
			g.pause.UnregisterCallback(g.pauseCallback)
			g.pauseCallback = nil
			g.access.Unlock()
			return
		}
		g.CheckOutbounds(false)
	}
}

func (g *LoadBalanceGroup) CheckOutbounds(force bool) {
	_, _ = g.urlTest(g.ctx, force)
}

func (g *LoadBalanceGroup) URLTest(ctx context.Context) (map[string]uint16, error) {
	return g.urlTest(ctx, false)
}

func (g *LoadBalanceGroup) urlTest(ctx context.Context, force bool) (map[string]uint16, error) {
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
		if !force && history != nil && time.Since(history.Time) < g.interval {
			continue
		}
		checked[realTag] = true
		p, loaded := g.provider.OutboundWithProvider(realTag)
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
	return result, nil
}

func (g *LoadBalanceGroup) Unwrap(metadata *adapter.InboundContext, touch bool) adapter.Outbound {
	return g.strategyFn(metadata, touch)
}

func (g *LoadBalanceGroup) AliveForTestUrl(proxy adapter.Outbound) bool {
	if history := g.history.LoadURLTestHistory(RealTag(proxy)); history != nil {
		return true
	}
	return false
}

func getKey(metadata *adapter.InboundContext) string {
	if metadata == nil {
		return ""
	}

	var metadataHost string
	if metadata.Destination.IsFqdn() {
		metadataHost = metadata.Destination.Fqdn
	} else if metadata.SniffHost != "" {
		metadataHost = metadata.SniffHost
	} else {
		metadataHost = metadata.Domain
	}

	if metadataHost != "" {
		// ip host
		if ip := net.ParseIP(metadataHost); ip != nil {
			return metadataHost
		}

		if etld, err := publicsuffix.EffectiveTLDPlusOne(metadataHost); err == nil {
			return etld
		}
	}

	var destinationAddr netip.Addr
	if len(metadata.DestinationAddresses) > 0 {
		destinationAddr = metadata.DestinationAddresses[0]
	} else {
		destinationAddr = metadata.Destination.Addr
	}

	if !destinationAddr.IsValid() {
		return ""
	}

	return destinationAddr.String()
}

func getKeyWithSrcAndDst(metadata *adapter.InboundContext) string {
	dst := getKey(metadata)
	src := ""
	if metadata != nil {
		src = metadata.Source.Addr.String()
	}

	return fmt.Sprintf("%s%s", src, dst)
}

func jumpHash(key uint64, buckets int32) int32 {
	var b, j int64

	for j < int64(buckets) {
		b = j
		key = key*2862933555777941757 + 1
		j = int64(float64(b+1) * (float64(int64(1)<<31) / float64((key>>33)+1)))
	}

	return int32(b)
}

func strategyRoundRobin(g *LoadBalanceGroup, url string) strategyFn {
	idx := 0
	idxMutex := sync.Mutex{}
	return func(metadata *adapter.InboundContext, touch bool) adapter.Outbound {
		idxMutex.Lock()
		defer idxMutex.Unlock()

		i := 0
		length := len(g.outbounds)

		if touch {
			defer func() {
				idx = (idx + i) % length
			}()
		}

		for ; i < length; i++ {
			id := (idx + i) % length
			proxy := g.outbounds[id]
			if g.AliveForTestUrl(proxy) {
				i++
				return proxy
			}
		}

		return g.outbounds[0]
	}
}

func strategyConsistentHashing(g *LoadBalanceGroup, url string) strategyFn {
	maxRetry := 5
	hash := maphash.NewHasher[string]()
	return func(metadata *adapter.InboundContext, touch bool) adapter.Outbound {
		key := hash.Hash(getKey(metadata))
		buckets := int32(len(g.outbounds))
		for i := 0; i < maxRetry; i, key = i+1, key+1 {
			idx := jumpHash(key, buckets)
			proxy := g.outbounds[idx]
			if g.AliveForTestUrl(proxy) {
				return proxy
			}
		}

		// when availability is poor, traverse the entire list to get the available nodes
		for _, proxy := range g.outbounds {
			if g.AliveForTestUrl(proxy) {
				return proxy
			}
		}

		return g.outbounds[0]
	}
}

func strategyStickySessions(g *LoadBalanceGroup, url string) strategyFn {
	maxRetry := 5
	lruCache := common.Must1(freelru.NewSharded[uint64, int](1000, maphash.NewHasher[uint64]().Hash32))
	lruCache.SetLifetime(g.ttl)
	hash := maphash.NewHasher[string]()
	return func(metadata *adapter.InboundContext, touch bool) adapter.Outbound {
		key := hash.Hash(getKeyWithSrcAndDst(metadata))
		length := len(g.outbounds)
		idx, has := lruCache.Get(key)
		if !has || idx >= length {
			idx = int(jumpHash(key+uint64(time.Now().UnixNano()), int32(length)))
		}

		nowIdx := idx
		for i := 1; i < maxRetry; i++ {
			proxy := g.outbounds[nowIdx]
			if g.AliveForTestUrl(proxy) {
				if !has || nowIdx != idx {
					lruCache.Add(key, nowIdx)
				}

				return proxy
			} else {
				nowIdx = int(jumpHash(key+uint64(time.Now().UnixNano()), int32(length)))
			}
		}

		lruCache.Add(key, 0)
		return g.outbounds[0]
	}
}
