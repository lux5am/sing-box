package option

type ExperimentalOptions struct {
	CacheFile *CacheFileOptions `json:"cache_file,omitempty"`
	ClashAPI  *ClashAPIOptions  `json:"clash_api,omitempty"`
	V2RayAPI  *V2RayAPIOptions  `json:"v2ray_api,omitempty"`
	Debug     *DebugOptions     `json:"debug,omitempty"`
	Timeout   *TimeoutOptions   `json:"timeout,omitempty"`
}

type CacheFileOptions struct {
	Enabled     bool     `json:"enabled,omitempty"`
	Path        string   `json:"path,omitempty"`
	CacheID     string   `json:"cache_id,omitempty"`
	StoreFakeIP bool     `json:"store_fakeip,omitempty"`
	StoreRDRC   bool     `json:"store_rdrc,omitempty"`
	RDRCTimeout Duration `json:"rdrc_timeout,omitempty"`
}

type ClashAPIOptions struct {
	ExternalController               string           `json:"external_controller,omitempty"`
	ExternalUI                       string           `json:"external_ui,omitempty"`
	ExternalUIDownloadURL            string           `json:"external_ui_download_url,omitempty"`
	ExternalUIDownloadDetour         string           `json:"external_ui_download_detour,omitempty"`
	Secret                           string           `json:"secret,omitempty"`
	DefaultMode                      string           `json:"default_mode,omitempty"`
	ModeList                         []string         `json:"-"`
	AccessControlAllowOrigin         Listable[string] `json:"access_control_allow_origin,omitempty"`
	AccessControlAllowPrivateNetwork bool             `json:"access_control_allow_private_network,omitempty"`

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
	TCPKeepAliveInitial        Duration `json:"tcp_keep_alive_initial,omitempty"`
	TCPKeepAliveInterval       Duration `json:"tcp_keep_alive_interval,omitempty"`
	TCPConnectTimeout          Duration `json:"tcp_connect_timeout,omitempty"`
	TCPTimeout                 Duration `json:"tcp_timeout,omitempty"`
	ReadPayloadTimeout         Duration `json:"read_payload_timeout,omitempty"`
	DNSTimeout                 Duration `json:"dns_timeout,omitempty"`
	QUICTimeout                Duration `json:"dns_timeout,omitempty"`
	STUNTimeout                Duration `json:"dns_timeout,omitempty"`
	UDPTimeout                 Duration `json:"udp_timeout,omitempty"`
	DefaultDonloadInterval     Duration `json:"default_download_interval,omitempty"`
	DefaultURLTestInterval     Duration `json:"default_urltest_interval,omitempty"`
	DefaultURLTestIdleTimeout  Duration `json:"default_urltest_idle_timeout,omitempty"`
	StartTimeout               Duration `json:"start_timeout,omitempty"`
	StopTimeout                Duration `json:"stop_timeout,omitempty"`
	FatalStopTimeout           Duration `json:"fatal_stop_timeout,omitempty"`
	FakeIPMetadataSaveInterval Duration `json:"fakeip_metadata_save_interval,omitempty"`
}
