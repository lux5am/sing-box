package option

import (
	"time"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common/json/badoption"
)

type ExperimentalOptions struct {
	CacheFile *CacheFileOptions `json:"cache_file,omitempty"`
	ClashAPI  *ClashAPIOptions  `json:"clash_api,omitempty"`
	V2RayAPI  *V2RayAPIOptions  `json:"v2ray_api,omitempty"`
	Debug     *DebugOptions     `json:"debug,omitempty"`
	Timeout   *TimeoutOptions   `json:"timeout,omitempty"`
}

func (o *ExperimentalOptions) Apply() {
	if o.Timeout != nil {
		o.Timeout.Apply()
	}
}

type CacheFileOptions struct {
	Enabled     bool               `json:"enabled,omitempty"`
	Path        string             `json:"path,omitempty"`
	CacheID     string             `json:"cache_id,omitempty"`
	StoreFakeIP bool               `json:"store_fakeip,omitempty"`
	StoreRDRC   bool               `json:"store_rdrc,omitempty"`
	RDRCTimeout badoption.Duration `json:"rdrc_timeout,omitempty"`
	StoreDNS    bool               `json:"store_dns,omitempty"`
}

type ClashAPIOptions struct {
	ExternalController               string                     `json:"external_controller,omitempty"`
	ExternalUI                       string                     `json:"external_ui,omitempty"`
	ExternalUIDownloadURL            string                     `json:"external_ui_download_url,omitempty"`
	ExternalUIDownloadDetour         string                     `json:"external_ui_download_detour,omitempty"`
	Secret                           string                     `json:"secret,omitempty"`
	DefaultMode                      string                     `json:"default_mode,omitempty"`
	ModeList                         []string                   `json:"-"`
	AccessControlAllowOrigin         badoption.Listable[string] `json:"access_control_allow_origin,omitempty"`
	AccessControlAllowPrivateNetwork bool                       `json:"access_control_allow_private_network,omitempty"`

	// Deprecated: migrated to global cache file
	CacheFile string `json:"cache_file,omitempty"`
	// Deprecated: migrated to global cache file
	CacheID string `json:"cache_id,omitempty"`
	// Deprecated: migrated to global cache file
	StoreMode bool `json:"store_mode,omitempty"`
	// Deprecated: migrated to global cache file
	StoreSelected bool `json:"store_selected,omitempty"`
	// Deprecated: migrated to global cache file
	StoreFakeIP bool `json:"store_fakeip,omitempty"`
}

type V2RayAPIOptions struct {
	Listen string                    `json:"listen,omitempty"`
	Stats  *V2RayStatsServiceOptions `json:"stats,omitempty"`
}

type V2RayStatsServiceOptions struct {
	Enabled   bool     `json:"enabled,omitempty"`
	Inbounds  []string `json:"inbounds,omitempty"`
	Outbounds []string `json:"outbounds,omitempty"`
	Users     []string `json:"users,omitempty"`
}

type TimeoutOptions struct {
	TCPKeepAliveInitial          badoption.Duration `json:"tcp_keep_alive_initial,omitempty"`
	TCPKeepAliveInterval         badoption.Duration `json:"tcp_keep_alive_interval,omitempty"`
	TCPKeepAliveCount            int                `json:"tcp_keep_alive_count,omitempty"`
	DisableTCPKeepAlive          bool               `json:"disable_tcp_keep_alive,omitempty"`
	TCPConnectTimeout            badoption.Duration `json:"tcp_connect_timeout,omitempty"`
	TCPTimeout                   badoption.Duration `json:"tcp_timeout,omitempty"`
	ReadPayloadTimeout           badoption.Duration `json:"read_payload_timeout,omitempty"`
	DNSTimeout                   badoption.Duration `json:"dns_timeout,omitempty"`
	UDPTimeout                   badoption.Duration `json:"udp_timeout,omitempty"`
	DefaultDonloadInterval       badoption.Duration `json:"default_download_interval,omitempty"`
	DefaultURLTestInterval       badoption.Duration `json:"default_urltest_interval,omitempty"`
	DefaultURLTestIdleTimeout    badoption.Duration `json:"default_urltest_idle_timeout,omitempty"`
	StartTimeout                 badoption.Duration `json:"start_timeout,omitempty"`
	StopTimeout                  badoption.Duration `json:"stop_timeout,omitempty"`
	FatalStopTimeout             badoption.Duration `json:"fatal_stop_timeout,omitempty"`
	FakeIPMetadataSaveInterval   badoption.Duration `json:"fakeip_metadata_save_interval,omitempty"`
	TLSFragmentFallbackDelay     badoption.Duration `json:"tls_fragment_fallback_delay,omitempty"`
	HTTPTransportIdleConnTimeout badoption.Duration `json:"http_transport_idle_conn_timeout,omitempty"`
	GRPCTransportIdleConnTimeout badoption.Duration `json:"grpc_transport_idle_conn_timeout,omitempty"`
	GRPCTransportCleanUpInterval badoption.Duration `json:"grpc_transport_cleanup_interval,omitempty"`
	ProtocolDNS                  badoption.Duration `json:"protocol_dns,omitempty"`
	ProtocolNTP                  badoption.Duration `json:"protocol_ntp,omitempty"`
	ProtocolSTUN                 badoption.Duration `json:"protocol_stun,omitempty"`
	ProtocolQUIC                 badoption.Duration `json:"protocol_quic,omitempty"`
	ProtocolDTLS                 badoption.Duration `json:"protocol_dtls,omitempty"`
}

func (o *TimeoutOptions) Apply() {
	C.DisableTCPKeepAlive = o.DisableTCPKeepAlive
	if o.TCPKeepAliveCount != 0 {
		C.TCPKeepAliveCount = o.TCPKeepAliveCount
	}
	if o.TCPKeepAliveInitial != 0 {
		C.TCPKeepAliveInitial = time.Duration(o.TCPKeepAliveInitial)
	}
	if o.TCPKeepAliveInterval != 0 {
		C.TCPKeepAliveInterval = time.Duration(o.TCPKeepAliveInterval)
	}
	if o.TCPConnectTimeout != 0 {
		C.TCPConnectTimeout = time.Duration(o.TCPConnectTimeout)
	}
	if o.TCPTimeout != 0 {
		C.TCPTimeout = time.Duration(o.TCPTimeout)
	}
	if o.ReadPayloadTimeout != 0 {
		C.ReadPayloadTimeout = time.Duration(o.ReadPayloadTimeout)
	}
	if o.DNSTimeout != 0 {
		C.DNSTimeout = time.Duration(o.DNSTimeout)
	}
	if o.UDPTimeout != 0 {
		C.UDPTimeout = time.Duration(o.UDPTimeout)
	}
	if o.DefaultDonloadInterval != 0 {
		C.DefaultDonloadInterval = time.Duration(o.DefaultDonloadInterval)
	}
	if o.DefaultURLTestInterval != 0 {
		C.DefaultURLTestInterval = time.Duration(o.DefaultURLTestInterval)
	}
	if o.DefaultURLTestIdleTimeout != 0 {
		C.DefaultURLTestIdleTimeout = time.Duration(o.DefaultURLTestIdleTimeout)
	}
	if o.StartTimeout != 0 {
		C.StartTimeout = time.Duration(o.StartTimeout)
	}
	if o.StopTimeout != 0 {
		C.StopTimeout = time.Duration(o.StopTimeout)
	}
	if o.FatalStopTimeout != 0 {
		C.FatalStopTimeout = time.Duration(o.FatalStopTimeout)
	}
	if o.FakeIPMetadataSaveInterval != 0 {
		C.FakeIPMetadataSaveInterval = time.Duration(o.FakeIPMetadataSaveInterval)
	}
	if o.TLSFragmentFallbackDelay != 0 {
		C.TLSFragmentFallbackDelay = time.Duration(o.TLSFragmentFallbackDelay)
	}
	if o.HTTPTransportIdleConnTimeout != 0 {
		C.HTTPTransportIdleConnTimeout = time.Duration(o.HTTPTransportIdleConnTimeout)
	}
	if o.GRPCTransportIdleConnTimeout != 0 {
		C.GRPCTransportIdleConnTimeout = time.Duration(o.GRPCTransportIdleConnTimeout)
	}
	if o.GRPCTransportCleanUpInterval != 0 {
		C.GRPCTransportCleanUpInterval = time.Duration(o.GRPCTransportCleanUpInterval)
	}
	if o.ProtocolDNS != 0 {
		C.ProtocolTimeouts[C.ProtocolDNS] = time.Duration(o.ProtocolDNS)
	}
	if o.ProtocolNTP != 0 {
		C.ProtocolTimeouts[C.ProtocolNTP] = time.Duration(o.ProtocolNTP)
	}
	if o.ProtocolSTUN != 0 {
		C.ProtocolTimeouts[C.ProtocolSTUN] = time.Duration(o.ProtocolSTUN)
	}
	if o.ProtocolQUIC != 0 {
		C.ProtocolTimeouts[C.ProtocolQUIC] = time.Duration(o.ProtocolQUIC)
	}
	if o.ProtocolDTLS != 0 {
		C.ProtocolTimeouts[C.ProtocolDTLS] = time.Duration(o.ProtocolDTLS)
	}
}
