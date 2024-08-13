package provider

import (
	"reflect"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	E "github.com/sagernet/sing/common/exceptions"
)

func (p *myProviderAdapter) newParser(content string) ([]option.Outbound, error) {
	var outbounds []option.Outbound
	var err error
	switch true {
	case strings.Contains(content, "\"outbounds\""):
		var options option.OutboundProviderOptions
		err = options.UnmarshalJSON([]byte(content))
		if err != nil {
			return nil, E.Cause(err, "decode config at ")
		}
		outbounds = options.Outbounds
	case strings.Contains(content, "proxies"):
		outbounds, err = newClashParser(content)
		if err != nil {
			return nil, err
		}
	default:
		outbounds, err = newNativeURIParser(content)
		if err != nil {
			return nil, err
		}
	}
	return p.overrideOutbounds(outbounds), nil
}

func (p *myProviderAdapter) overrideOutbounds(outbounds []option.Outbound) []option.Outbound {
	var tags []string
	for _, outbound := range outbounds {
		tags = append(tags, outbound.Tag)
	}
	var parsedOutbounds []option.Outbound
	for _, outbound := range outbounds {
		if p.outboundOverride != nil {
			if p.outboundOverride.TagPrefix != "" {
				outbound.Tag = p.outboundOverride.TagPrefix + outbound.Tag
			}
			if p.outboundOverride.TagSuffix != "" {
				outbound.Tag = outbound.Tag + p.outboundOverride.TagSuffix
			}
		}
		switch outbound.Type {
		case C.TypeHTTP:
			options := outbound.Options.(option.HTTPOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeSOCKS:
			options := outbound.Options.(option.SOCKSOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeTUIC:
			options := outbound.Options.(option.TUICOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeVMess:
			options := outbound.Options.(option.VMessOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeVLESS:
			options := outbound.Options.(option.VLESSOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeTrojan:
			options := outbound.Options.(option.TrojanOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeHysteria:
			options := outbound.Options.(option.HysteriaOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeShadowTLS:
			options := outbound.Options.(option.ShadowTLSOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeHysteria2:
			options := outbound.Options.(option.Hysteria2OutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeWireGuard:
			options := outbound.Options.(option.LegacyWireGuardOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeShadowsocks:
			options := outbound.Options.(option.ShadowsocksOutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		case C.TypeShadowsocksR:
			options := outbound.Options.(option.ShadowsocksROutboundOptions)
			options.DialerOptions = p.overrideDialerOption(options.DialerOptions, tags)
			outbound.Options = &options
		}
		parsedOutbounds = append(parsedOutbounds, outbound)
	}
	return parsedOutbounds
}

func (p *myProviderAdapter) overrideDialerOption(options option.DialerOptions, tags []string) option.DialerOptions {
	if options.Detour != "" && !common.Any(tags, func(tag string) bool {
		return options.Detour == tag
	}) {
		options.Detour = ""
	}
	var defaultOptions option.OverrideDialerOptions
	if p.outboundOverride == nil || p.outboundOverride.OverrideDialerOptions == nil || reflect.DeepEqual(*p.outboundOverride.OverrideDialerOptions, defaultOptions) {
		return options
	}
	if p.outboundOverride.OverrideDialerOptions.Detour != nil && options.Detour == "" {
		options.Detour = *p.outboundOverride.OverrideDialerOptions.Detour
	}
	if p.outboundOverride.OverrideDialerOptions.BindInterface != nil {
		options.BindInterface = *p.outboundOverride.OverrideDialerOptions.BindInterface
	}
	if p.outboundOverride.OverrideDialerOptions.Inet4BindAddress != nil {
		options.Inet4BindAddress = p.outboundOverride.OverrideDialerOptions.Inet4BindAddress
	}
	if p.outboundOverride.OverrideDialerOptions.Inet6BindAddress != nil {
		options.Inet6BindAddress = p.outboundOverride.OverrideDialerOptions.Inet6BindAddress
	}
	if p.outboundOverride.OverrideDialerOptions.ProtectPath != nil {
		options.ProtectPath = *p.outboundOverride.OverrideDialerOptions.ProtectPath
	}
	if p.outboundOverride.OverrideDialerOptions.RoutingMark != nil {
		options.RoutingMark = *p.outboundOverride.OverrideDialerOptions.RoutingMark
	}
	if p.outboundOverride.OverrideDialerOptions.ReuseAddr != nil {
		options.ReuseAddr = *p.outboundOverride.OverrideDialerOptions.ReuseAddr
	}
	if p.outboundOverride.OverrideDialerOptions.ConnectTimeout != nil {
		options.ConnectTimeout = *p.outboundOverride.OverrideDialerOptions.ConnectTimeout
	}
	if p.outboundOverride.OverrideDialerOptions.TCPFastOpen != nil {
		options.TCPFastOpen = *p.outboundOverride.OverrideDialerOptions.TCPFastOpen
	}
	if p.outboundOverride.OverrideDialerOptions.TCPMultiPath != nil {
		options.TCPMultiPath = *p.outboundOverride.OverrideDialerOptions.TCPMultiPath
	}
	if p.outboundOverride.OverrideDialerOptions.UDPFragment != nil {
		options.UDPFragment = p.outboundOverride.OverrideDialerOptions.UDPFragment
	}
	if p.outboundOverride.OverrideDialerOptions.DomainStrategy != nil {
		options.UDPFragment = p.outboundOverride.OverrideDialerOptions.UDPFragment
	}
	if p.outboundOverride.OverrideDialerOptions.FallbackDelay != nil {
		options.FallbackDelay = *p.outboundOverride.OverrideDialerOptions.FallbackDelay
	}
	return options
}
