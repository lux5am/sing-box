package dns

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/taskmonitor"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/experimental/libbox/platform"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	R "github.com/sagernet/sing-box/route/rule"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/contrab/freelru"
	"github.com/sagernet/sing/contrab/maphash"
	"github.com/sagernet/sing/service"

	mDNS "github.com/miekg/dns"
)

var _ adapter.DNSRouter = (*Router)(nil)

type Router struct {
	ctx                   context.Context
	logger                logger.ContextLogger
	transport             adapter.DNSTransportManager
	outbound              adapter.OutboundManager
	client                adapter.DNSClient
	rules                 []adapter.DNSRule
	defaultDomainStrategy C.DomainStrategy
	dnsReverseMapping     freelru.Cache[netip.Addr, string]
	platformInterface     platform.Interface
}

func NewRouter(ctx context.Context, logFactory log.Factory, options option.DNSOptions) *Router {
	var dnsHosts *Hosts
	if len(options.Hosts) > 0 {
		var err error
		hostsMap := make(map[string][]string)
		for domain, hosts := range options.Hosts {
			hostsMap[domain] = hosts
		}
		dnsHosts, err = NewHosts(hostsMap)
		if err != nil {
			dnsHosts = nil
		}
	}
	router := &Router{
		ctx:                   ctx,
		logger:                logFactory.NewLogger("dns"),
		transport:             service.FromContext[adapter.DNSTransportManager](ctx),
		outbound:              service.FromContext[adapter.OutboundManager](ctx),
		rules:                 make([]adapter.DNSRule, 0, len(options.Rules)),
		defaultDomainStrategy: C.DomainStrategy(options.Strategy),
	}
	router.client = NewClient(ClientOptions{
		DisableCache:     options.DNSClientOptions.DisableCache,
		DisableExpire:    options.DNSClientOptions.DisableExpire,
		IndependentCache: options.DNSClientOptions.IndependentCache,
		RoundRobinCache:  options.DNSClientOptions.RoundRobinCache,
		StaleCache:       options.DNSClientOptions.StaleCache,
		CacheCapacity:    options.DNSClientOptions.CacheCapacity,
		MinCacheTTL:      options.DNSClientOptions.MinCacheTTL,
		MaxCacheTTL:      options.DNSClientOptions.MaxCacheTTL,
		Hosts:            dnsHosts,
		RDRC: func() adapter.RDRCStore {
			cacheFile := service.FromContext[adapter.CacheFile](ctx)
			if cacheFile == nil {
				return nil
			}
			if !cacheFile.StoreRDRC() {
				return nil
			}
			return cacheFile
		},
		Logger: router.logger,
	})
	if options.ReverseMapping {
		router.dnsReverseMapping = common.Must1(freelru.NewSharded[netip.Addr, string](1024, maphash.NewHasher[netip.Addr]().Hash32))
	}
	return router
}

func (r *Router) Initialize(rules []option.DNSRule) error {
	for i, ruleOptions := range rules {
		dnsRule, err := R.NewDNSRule(r.ctx, r.logger, ruleOptions, true)
		if err != nil {
			return E.Cause(err, "parse dns rule[", i, "]")
		}
		r.rules = append(r.rules, dnsRule)
	}
	return nil
}

func (r *Router) Start(stage adapter.StartStage) error {
	monitor := taskmonitor.New(r.logger, C.StartTimeout)
	switch stage {
	case adapter.StartStateStart:
		monitor.Start("initialize DNS client")
		r.client.Start()
		monitor.Finish()

		for i, rule := range r.rules {
			monitor.Start("initialize DNS rule[", i, "]")
			err := rule.Start()
			monitor.Finish()
			if err != nil {
				return E.Cause(err, "initialize DNS rule[", i, "]")
			}
		}
	}
	return nil
}

func (r *Router) Close() error {
	monitor := taskmonitor.New(r.logger, C.StopTimeout)
	var err error
	for i, rule := range r.rules {
		monitor.Start("close dns rule[", i, "]")
		err = E.Append(err, rule.Close(), func(err error) error {
			return E.Cause(err, "close dns rule[", i, "]")
		})
		monitor.Finish()
	}
	return err
}

func (r *Router) Client() adapter.DNSClient {
	return r.client
}

func (r *Router) DefaultDomainStrategy() C.DomainStrategy {
	return r.defaultDomainStrategy
}

func (r *Router) matchDNS(ctx context.Context, allowFakeIP bool, ruleIndex int, isAddressQuery bool, options *adapter.DNSQueryOptions) (adapter.DNSTransport, adapter.DNSRule, int) {
	metadata := adapter.ContextFrom(ctx)
	if metadata == nil {
		panic("no context")
	}
	var currentRuleIndex int
	if ruleIndex != -1 {
		currentRuleIndex = ruleIndex + 1
	}
	for ; currentRuleIndex < len(r.rules); currentRuleIndex++ {
		currentRule := r.rules[currentRuleIndex]
		if currentRule.WithAddressLimit() && !isAddressQuery {
			continue
		}
		metadata.ResetRuleCache()
		if currentRule.Match(metadata) {
			displayRuleIndex := currentRuleIndex
			if displayRuleIndex != -1 {
				displayRuleIndex += displayRuleIndex + 1
			}
			ruleDescription := currentRule.String()
			if ruleDescription != "" {
				r.logger.DebugContext(ctx, "match[", displayRuleIndex, "] ", currentRule, " => ", currentRule.Action())
			} else {
				r.logger.DebugContext(ctx, "match[", displayRuleIndex, "] => ", currentRule.Action())
			}
			switch action := currentRule.Action().(type) {
			case *R.RuleActionDNSRoute:
				transport, loaded := r.transport.Transport(action.Server)
				if !loaded {
					r.logger.ErrorContext(ctx, "transport not found: ", action.Server)
					continue
				}
				isFakeIP := transport.Type() == C.DNSTypeFakeIP
				if isFakeIP && !allowFakeIP {
					continue
				}
				if action.Strategy != C.DomainStrategyAsIS {
					options.Strategy = action.Strategy
				}
				if isFakeIP || action.DisableCache {
					options.DisableCache = true
				}
				if action.RewriteTTL != nil {
					options.RewriteTTL = action.RewriteTTL
				}
				if action.ClientSubnet.IsValid() {
					options.ClientSubnet = action.ClientSubnet
				}
				if legacyTransport, isLegacy := transport.(adapter.LegacyDNSTransport); isLegacy {
					if options.Strategy == C.DomainStrategyAsIS {
						options.Strategy = legacyTransport.LegacyStrategy()
					}
					if !options.ClientSubnet.IsValid() {
						options.ClientSubnet = legacyTransport.LegacyClientSubnet()
					}
				}
				return transport, currentRule, currentRuleIndex
			case *R.RuleActionDNSRouteOptions:
				if action.Strategy != C.DomainStrategyAsIS {
					options.Strategy = action.Strategy
				}
				if action.DisableCache {
					options.DisableCache = true
				}
				if action.RewriteTTL != nil {
					options.RewriteTTL = action.RewriteTTL
				}
				if action.ClientSubnet.IsValid() {
					options.ClientSubnet = action.ClientSubnet
				}
			case *R.RuleActionReject:
				return nil, currentRule, currentRuleIndex
			}
		}
	}
	return r.transport.Default(), nil, -1
}

func (r *Router) Exchange(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions) (*mDNS.Msg, error) {
	if len(message.Question) != 1 {
		r.logger.WarnContext(ctx, "bad question size: ", len(message.Question))
		responseMessage := mDNS.Msg{
			MsgHdr: mDNS.MsgHdr{
				Id:       message.Id,
				Response: true,
				Rcode:    mDNS.RcodeFormatError,
			},
			Question: message.Question,
		}
		return &responseMessage, nil
	}
	r.logger.DebugContext(ctx, "exchange ", FormatQuestion(message.Question[0].String()))
	var (
		transport adapter.DNSTransport
		err       error
		response  *mDNS.Msg
		records   []mDNS.RR
		cached    bool
	)
	rawFqdn := message.Question[0].Name
	if response, records = r.client.SearchCNAMEHosts(ctx, message); response != nil {
		return response, nil
	}
	if len(records) > 0 {
		defer func() {
			if err != nil || response == nil || len(response.Answer) == 0 {
				return
			}
			if response.Question[0].Name != rawFqdn {
				response.Question[0].Name = rawFqdn
				response.Answer = append(records, response.Answer...)
			}
		}()
	}
	if options.Strategy == C.DomainStrategyAsIS {
		options.Strategy = r.defaultDomainStrategy
	}
	if response = r.client.SearchIPHosts(ctx, message, options.Strategy); response != nil {
		return response, nil
	}
	if !r.client.UpdateDnsCacheFromContext(ctx) {
		var needUpdate bool
		if response, cached, needUpdate = r.client.ExchangeCache(ctx, message); cached {
			if needUpdate {
				go func(ctx context.Context, message *mDNS.Msg, options adapter.DNSQueryOptions) {
					r.Exchange(r.client.UpdateDnsCacheToContext(ctx), message, options)
				}(ctx, message, options)
			}
			return response, nil
		}
	}
	if !cached {
		var metadata *adapter.InboundContext
		ctx, metadata = adapter.ExtendContext(ctx)
		metadata.Destination = M.Socksaddr{}
		metadata.QueryType = message.Question[0].Qtype
		switch metadata.QueryType {
		case mDNS.TypeA:
			metadata.IPVersion = 4
		case mDNS.TypeAAAA:
			metadata.IPVersion = 6
		}
		metadata.Domain = FqdnToDomain(message.Question[0].Name)
		if options.Transport != nil {
			transport = options.Transport
			if legacyTransport, isLegacy := transport.(adapter.LegacyDNSTransport); isLegacy {
				if options.Strategy == C.DomainStrategyAsIS {
					options.Strategy = legacyTransport.LegacyStrategy()
				}
				if !options.ClientSubnet.IsValid() {
					options.ClientSubnet = legacyTransport.LegacyClientSubnet()
				}
			}
			if options.Strategy == C.DomainStrategyAsIS {
				options.Strategy = r.defaultDomainStrategy
			}
			response, err = r.client.Exchange(ctx, transport, message, options, nil)
		} else {
			var (
				rule      adapter.DNSRule
				ruleIndex int
			)
			ruleIndex = -1
			for {
				dnsCtx := adapter.OverrideContext(ctx)
				dnsOptions := options
				transport, rule, ruleIndex = r.matchDNS(ctx, true, ruleIndex, isAddressQuery(message), &dnsOptions)
				if rule != nil {
					switch action := rule.Action().(type) {
					case *R.RuleActionReject:
						switch action.Method {
						case C.RuleActionRejectMethodDefault:
							return FixedResponse(message.Id, message.Question[0], nil, 0), nil
						case C.RuleActionRejectMethodDrop:
							return nil, tun.ErrDrop
						}
					}
				}
				var responseCheck func(responseAddrs []netip.Addr) bool
				if rule != nil && rule.WithAddressLimit() {
					responseCheck = func(responseAddrs []netip.Addr) bool {
						metadata.DestinationAddresses = responseAddrs
						return rule.MatchAddressLimit(metadata)
					}
				}
				if dnsOptions.Strategy == C.DomainStrategyAsIS {
					dnsOptions.Strategy = r.defaultDomainStrategy
				}
				response, err = r.client.Exchange(dnsCtx, transport, message, dnsOptions, responseCheck)
				var rejected bool
				if err != nil {
					if errors.Is(err, ErrResponseRejectedCached) {
						rejected = true
						r.logger.DebugContext(ctx, E.Cause(err, "response rejected for ", FormatQuestion(message.Question[0].String())), " (cached)")
					} else if errors.Is(err, ErrResponseRejected) {
						rejected = true
						r.logger.DebugContext(ctx, E.Cause(err, "response rejected for ", FormatQuestion(message.Question[0].String())))
					} else if len(message.Question) > 0 {
						r.logger.ErrorContext(ctx, E.Cause(err, "exchange failed for ", FormatQuestion(message.Question[0].String())))
					} else {
						r.logger.ErrorContext(ctx, E.Cause(err, "exchange failed for <empty query>"))
					}
				}
				if responseCheck != nil && rejected {
					continue
				}
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if r.dnsReverseMapping != nil && len(message.Question) > 0 && response != nil && len(response.Answer) > 0 {
		if transport == nil || transport.Type() != C.DNSTypeFakeIP {
			for _, answer := range response.Answer {
				switch record := answer.(type) {
				case *mDNS.A:
					r.dnsReverseMapping.AddWithLifetime(M.AddrFromIP(record.A), FqdnToDomain(record.Hdr.Name), time.Duration(record.Hdr.Ttl)*time.Second)
				case *mDNS.AAAA:
					r.dnsReverseMapping.AddWithLifetime(M.AddrFromIP(record.AAAA), FqdnToDomain(record.Hdr.Name), time.Duration(record.Hdr.Ttl)*time.Second)
				}
			}
		}
	}
	return response, nil
}

func (r *Router) Lookup(ctx context.Context, domain string, options adapter.DNSQueryOptions) ([]netip.Addr, error) {
	domain = r.client.GetExactDomainFromHosts(ctx, domain, false)
	if responseAddrs := r.client.GetAddrsFromHosts(ctx, domain, options.Strategy, false); len(responseAddrs) > 0 {
		return responseAddrs, nil
	}
	var (
		responseAddrs []netip.Addr
		cached        bool
		err           error
	)
	printResult := func() {
		if err != nil {
			if errors.Is(err, ErrResponseRejectedCached) {
				r.logger.DebugContext(ctx, "response rejected for ", domain, " (cached)")
			} else if errors.Is(err, ErrResponseRejected) {
				r.logger.DebugContext(ctx, "response rejected for ", domain)
			} else {
				r.logger.ErrorContext(ctx, E.Cause(err, "lookup failed for ", domain))
			}
		} else if len(responseAddrs) == 0 {
			r.logger.ErrorContext(ctx, "lookup failed for ", domain, ": empty result")
			err = RCodeNameError
		}
	}
	if !r.client.UpdateDnsCacheFromContext(ctx) {
		var needUpdate bool
		if responseAddrs, cached, needUpdate = r.client.LookupCache(domain, options.Strategy); cached {
			if needUpdate {
				go func(ctx context.Context, domain string, options adapter.DNSQueryOptions) {
					r.Lookup(r.client.UpdateDnsCacheToContext(ctx), domain, options)
				}(ctx, domain, options)
			}
			if len(responseAddrs) == 0 {
				return nil, RCodeNameError
			}
			return responseAddrs, nil
		}
	}
	r.logger.DebugContext(ctx, "lookup domain ", domain)
	ctx, metadata := adapter.ExtendContext(ctx)
	metadata.Destination = M.Socksaddr{}
	metadata.Domain = FqdnToDomain(domain)
	if options.Transport != nil {
		transport := options.Transport
		if legacyTransport, isLegacy := transport.(adapter.LegacyDNSTransport); isLegacy {
			if options.Strategy == C.DomainStrategyAsIS {
				options.Strategy = r.defaultDomainStrategy
			}
			if !options.ClientSubnet.IsValid() {
				options.ClientSubnet = legacyTransport.LegacyClientSubnet()
			}
		}
		if options.Strategy == C.DomainStrategyAsIS {
			options.Strategy = r.defaultDomainStrategy
		}
		responseAddrs, err = r.client.Lookup(ctx, transport, domain, options, nil)
	} else {
		var (
			transport adapter.DNSTransport
			rule      adapter.DNSRule
			ruleIndex int
		)
		ruleIndex = -1
		for {
			dnsCtx := adapter.OverrideContext(ctx)
			transport, rule, ruleIndex = r.matchDNS(ctx, false, ruleIndex, true, &options)
			if rule != nil {
				switch action := rule.Action().(type) {
				case *R.RuleActionReject:
					switch action.Method {
					case C.RuleActionRejectMethodDefault:
						return nil, nil
					case C.RuleActionRejectMethodDrop:
						return nil, tun.ErrDrop
					}
				}
			}
			var responseCheck func(responseAddrs []netip.Addr) bool
			if rule != nil && rule.WithAddressLimit() {
				responseCheck = func(responseAddrs []netip.Addr) bool {
					metadata.DestinationAddresses = responseAddrs
					return rule.MatchAddressLimit(metadata)
				}
			}
			if options.Strategy == C.DomainStrategyAsIS {
				options.Strategy = r.defaultDomainStrategy
			}
			responseAddrs, err = r.client.Lookup(dnsCtx, transport, domain, options, responseCheck)
			if responseCheck == nil || err == nil {
				break
			}
			printResult()
		}
	}
	printResult()
	if len(responseAddrs) > 0 {
		r.logger.InfoContext(ctx, "lookup succeed for ", domain, ": ", strings.Join(F.MapToString(responseAddrs), " "))
	}
	return responseAddrs, err
}

func isAddressQuery(message *mDNS.Msg) bool {
	for _, question := range message.Question {
		if question.Qtype == mDNS.TypeA || question.Qtype == mDNS.TypeAAAA || question.Qtype == mDNS.TypeHTTPS {
			return true
		}
	}
	return false
}

func (r *Router) ClearCache() {
	r.client.ClearCache()
	if r.platformInterface != nil {
		r.platformInterface.ClearDNSCache()
	}
}

func (r *Router) LookupReverseMapping(ip netip.Addr) (string, bool) {
	if r.dnsReverseMapping == nil {
		return "", false
	}
	domain, loaded := r.dnsReverseMapping.Get(ip)
	return domain, loaded
}

func (r *Router) ResetNetwork() {
	r.ClearCache()
	for _, transport := range r.transport.Transports() {
		transport.Reset()
	}
}
