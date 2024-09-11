package option

import "github.com/sagernet/sing/common/json/badoption"

type RouteOptions struct {
	GeoIP                      *GeoIPOptions                     `json:"geoip,omitempty"`
	Geosite                    *GeositeOptions                   `json:"geosite,omitempty"`
	Rules                      []Rule                            `json:"rules,omitempty"`
	RuleSet                    []RuleSet                         `json:"rule_set,omitempty"`
	Final                      string                            `json:"final,omitempty"`
	FindProcess                bool                              `json:"find_process,omitempty"`
	AutoDetectInterface        bool                              `json:"auto_detect_interface,omitempty"`
	OverrideAndroidVPN         bool                              `json:"override_android_vpn,omitempty"`
	DefaultInterface           string                            `json:"default_interface,omitempty"`
	DefaultMark                FwMark                            `json:"default_mark,omitempty"`
	DefaultNetworkStrategy     NetworkStrategy                   `json:"default_network_strategy,omitempty"`
	DefaultNetworkType         badoption.Listable[InterfaceType] `json:"default_network_type,omitempty"`
	DefaultFallbackNetworkType badoption.Listable[InterfaceType] `json:"default_fallback_network_type,omitempty"`
	DefaultFallbackDelay       badoption.Duration                `json:"default_fallback_delay,omitempty"`
	DisableTCPKeepAlive        bool                              `json:"disable_tcp_keep_alive,omitempty"`
	TCPKeepAliveInitial        badoption.Duration                `json:"tcp_keep_alive_initial,omitempty"`
	TCPKeepAliveInterval       badoption.Duration                `json:"tcp_keep_alive_interval,omitempty"`
}

type GeoIPOptions struct {
	Path           string `json:"path,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	DownloadDetour string `json:"download_detour,omitempty"`
}

type GeositeOptions struct {
	Path           string `json:"path,omitempty"`
	DownloadURL    string `json:"download_url,omitempty"`
	DownloadDetour string `json:"download_detour,omitempty"`
}
