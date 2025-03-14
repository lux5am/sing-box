package option

import "github.com/sagernet/sing/common/json/badoption"

type ExperimentalOptions struct {
	CacheFile *CacheFileOptions `json:"cache_file,omitempty"`
	ClashAPI  *ClashAPIOptions  `json:"clash_api,omitempty"`
	V2RayAPI  *V2RayAPIOptions  `json:"v2ray_api,omitempty"`
	Debug     *DebugOptions     `json:"debug,omitempty"`
	Timeout   *TimeoutOptions   `json:"timeout,omitempty"`
	Constant  *ConstantOptions  `json:"constant,omitempty"`
	URLTestUnifiedDelay bool    `json:"urltest_unified_delay,omitempty"`
}

type CacheFileOptions struct {
	Enabled     bool               `json:"enabled,omitempty"`
	Path        string             `json:"path,omitempty"`
	CacheID     string             `json:"cache_id,omitempty"`
	StoreFakeIP bool               `json:"store_fakeip,omitempty"`
	StoreRDRC   bool               `json:"store_rdrc,omitempty"`
	RDRCTimeout badoption.Duration `json:"rdrc_timeout,omitempty"`
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
	DisableTCPKeepAlive        bool               `json:"disable_tcp_keep_alive",omitempty"`
	TCPKeepAliveCount          int                `json:"tcp_keep_alive_count,omitempty"`
	TCPKeepAliveInitial        badoption.Duration `json:"tcp_keep_alive_initial,omitempty"`
	TCPKeepAliveInterval       badoption.Duration `json:"tcp_keep_alive_interval,omitempty"`
	TCPConnectTimeout          badoption.Duration `json:"tcp_connect_timeout,omitempty"`
	TCPTimeout                 badoption.Duration `json:"tcp_timeout,omitempty"`
	ReadPayloadTimeout         badoption.Duration `json:"read_payload_timeout,omitempty"`
	DNSTimeout                 badoption.Duration `json:"dns_timeout,omitempty"`
	UDPTimeout                 badoption.Duration `json:"udp_timeout,omitempty"`
	DefaultDonloadInterval     badoption.Duration `json:"default_download_interval,omitempty"`
	DefaultURLTestInterval     badoption.Duration `json:"default_urltest_interval,omitempty"`
	DefaultURLTestIdleTimeout  badoption.Duration `json:"default_urltest_idle_timeout,omitempty"`
	StartTimeout               badoption.Duration `json:"start_timeout,omitempty"`
	StopTimeout                badoption.Duration `json:"stop_timeout,omitempty"`
	FatalStopTimeout           badoption.Duration `json:"fatal_stop_timeout,omitempty"`
	FakeIPMetadataSaveInterval badoption.Duration `json:"fakeip_metadata_save_interval,omitempty"`
	TLSFragmentFallbackDelay   badoption.Duration `json:"tls_fragment_fallback_delay,omitempty"`
	ProtocolDNS                badoption.Duration `json:"protocol_dns,omitempty"`
	ProtocolNTP                badoption.Duration `json:"protocol_ntp,omitempty"`
	ProtocolSTUN               badoption.Duration `json:"protocol_stun,omitempty"`
	ProtocolQUIC               badoption.Duration `json:"protocol_quic,omitempty"`
	ProtocolDTLS               badoption.Duration `json:"protocol_dtls,omitempty"`
}

type ConstantOptions struct {
	DefaultDNSTTL uint32 `json:"default_dns_ttl,omitempty"`
}
