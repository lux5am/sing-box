package option

import "github.com/sagernet/sing/common/json/badoption"

type FilterOptions struct {
	Includes badoption.Listable[string] `json:"includes,omitempty"`
	Excludes string                     `json:"excludes,omitempty"`
	Types    badoption.Listable[string] `json:"types,omitempty"`
	Ports    badoption.Listable[string] `json:"ports,omitempty"`
}

type GroupOutboundOptions struct {
	Outbounds       badoption.Listable[string] `json:"outbounds,omitempty"`
	Providers       badoption.Listable[string] `json:"providers,omitempty"`
	UseAllProviders bool                       `json:"use_all_providers,omitempty"`
	FilterOptions
}

type SelectorOutboundOptions struct {
	GroupOutboundOptions
	Default                   string `json:"default,omitempty"`
	FallbackByDelayTest       bool   `json:"fallback_by_delay_test,omitempty"`
	InterruptExistConnections bool   `json:"interrupt_exist_connections,omitempty"`
}

type URLTestOutboundOptions struct {
	GroupOutboundOptions
	URL                       string             `json:"url,omitempty"`
	Interval                  badoption.Duration `json:"interval,omitempty"`
	Tolerance                 uint16             `json:"tolerance,omitempty"`
	IdleTimeout               badoption.Duration `json:"idle_timeout,omitempty"`
	InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
}

type FallbackOutboundOptions struct {
	GroupOutboundOptions
	URL                       string             `json:"url,omitempty"`
	Interval                  badoption.Duration `json:"interval,omitempty"`
	MaxDelay                  badoption.Duration `json:"max_delay,omitempty"`
	IdleTimeout               badoption.Duration `json:"idle_timeout,omitempty"`
	InterruptExistConnections bool               `json:"interrupt_exist_connections,omitempty"`
}
