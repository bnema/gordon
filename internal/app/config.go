package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/publictls"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
	"github.com/bnema/gordon/internal/usecase/traffic"
)

// Config holds the application configuration.
type Config struct {
	Server struct {
		Port                  int      `mapstructure:"port"`
		RegistryPort          int      `mapstructure:"registry_port"`
		GordonDomain          string   `mapstructure:"gordon_domain"`
		RegistryDomain        string   `mapstructure:"registry_domain"`
		LegacyRegistryDomains []string `mapstructure:"legacy_registry_domains"`
		TLSPort               int      `mapstructure:"tls_port"`
		TLSCertFile           string   `mapstructure:"tls_cert_file"`
		TLSKeyFile            string   `mapstructure:"tls_key_file"`
		ForceHTTPSRedirect    bool     `mapstructure:"force_https_redirect"`
		DataDir               string   `mapstructure:"data_dir"`
		MaxProxyBodySize      string   `mapstructure:"max_proxy_body_size"`     // e.g., "512MB", "1GB"
		MaxBlobChunkSize      string   `mapstructure:"max_blob_chunk_size"`     // e.g., "512MB", "1GB"
		MaxBlobSize           string   `mapstructure:"max_blob_size"`           // e.g., "1GB", "2GB"
		MaxProxyResponseSize  string   `mapstructure:"max_proxy_response_size"` // e.g., "1GB", "0" for no limit
		MaxConcurrentConns    int      `mapstructure:"max_concurrent_connections"`
		RegistryAllowedIPs    []string `mapstructure:"registry_allowed_ips"`
		ProxyAllowedIPs       []string `mapstructure:"proxy_allowed_ips"`
		RegistryListenAddr    string   `mapstructure:"registry_listen_address"`
	} `mapstructure:"server"`

	Apps struct {
		// RevisionRetention bounds unreferenced-revision GC per app.
		// Zero or negative restores the compiled default (8).
		RevisionRetention int `mapstructure:"revision_retention"`
	} `mapstructure:"apps"`

	// AppMounts declares administrative bind policies keyed by stable mount
	// name, referenced by name from an app manifest's [[services.<name>.bind]].
	AppMounts map[string]AppMountPolicy `mapstructure:"app_mounts"`

	// AppDevices declares administrative device grants keyed by stable
	// logical device name, referenced by name from an app manifest's
	// `devices` list.
	AppDevices map[string]AppDevicePolicy `mapstructure:"app_devices"`

	Logging struct {
		Level  string `mapstructure:"level"`
		Format string `mapstructure:"format"`
		File   struct {
			Enabled    bool   `mapstructure:"enabled"`
			Path       string `mapstructure:"path"`
			MaxSize    int    `mapstructure:"max_size"`
			MaxBackups int    `mapstructure:"max_backups"`
			MaxAge     int    `mapstructure:"max_age"`
		} `mapstructure:"file"`
		ContainerLogs struct {
			Enabled    bool   `mapstructure:"enabled"`
			Dir        string `mapstructure:"dir"`
			MaxSize    int    `mapstructure:"max_size"`
			MaxBackups int    `mapstructure:"max_backups"`
			MaxAge     int    `mapstructure:"max_age"`
		} `mapstructure:"container_logs"`
		AccessLog struct {
			Enabled             bool   `mapstructure:"enabled"`
			Format              string `mapstructure:"format"`
			Output              string `mapstructure:"output"`
			FilePath            string `mapstructure:"file_path"`
			MaxSize             int    `mapstructure:"max_size"`
			MaxBackups          int    `mapstructure:"max_backups"`
			MaxAge              int    `mapstructure:"max_age"`
			ExcludeHealthChecks bool   `mapstructure:"exclude_health_checks"`
			SyslogIdentifier    string `mapstructure:"syslog_identifier"`
		} `mapstructure:"access_log"`
	} `mapstructure:"logging"`

	Volumes struct {
		AutoCreate bool   `mapstructure:"auto_create"`
		Prefix     string `mapstructure:"prefix"`
		Preserve   bool   `mapstructure:"preserve"`
	} `mapstructure:"volumes"`

	Auth struct {
		Enabled        bool   `mapstructure:"enabled"`
		Type           string `mapstructure:"type"`            // only "token" is supported
		SecretsBackend string `mapstructure:"secrets_backend"` // "pass", "sops", or "unsafe"
		Username       string `mapstructure:"username"`
		TokenSecret    string `mapstructure:"token_secret"`     // path in secrets backend
		TokenExpiry    string `mapstructure:"token_expiry"`     // e.g., "720h", "30d"
		AccessTokenTTL string `mapstructure:"access_token_ttl"` // e.g., "15m", "30m" (default: 15m)
	} `mapstructure:"auth"`

	API struct {
		RateLimit struct {
			Enabled        bool     `mapstructure:"enabled"`
			GlobalRPS      float64  `mapstructure:"global_rps"`
			PerIPRPS       float64  `mapstructure:"per_ip_rps"`
			Burst          int      `mapstructure:"burst"`
			TrustedProxies []string `mapstructure:"trusted_proxies"`
		} `mapstructure:"rate_limit"`
	} `mapstructure:"api"`

	EntryPoints     map[string]traffic.EntryPointConfig `mapstructure:"entrypoints"`
	Traffic         traffic.Config                      `mapstructure:"traffic"`
	NetworkServices []traffic.NetworkServiceConfig      `mapstructure:"network_services"`
	Services        []servicecfg.Config                 `mapstructure:"services"`

	Backups struct {
		// Legacy database backup keys. Prefer backups.databases.* for new configs.
		Enabled    bool   `mapstructure:"enabled"`
		Schedule   string `mapstructure:"schedule"`
		StorageDir string `mapstructure:"storage_dir"`
		Retention  struct {
			Hourly  int `mapstructure:"hourly"`
			Daily   int `mapstructure:"daily"`
			Weekly  int `mapstructure:"weekly"`
			Monthly int `mapstructure:"monthly"`
		} `mapstructure:"retention"`
		Databases struct {
			Enabled    bool   `mapstructure:"enabled"`
			Schedule   string `mapstructure:"schedule"`
			StorageDir string `mapstructure:"storage_dir"`
			Retention  struct {
				Hourly  int `mapstructure:"hourly"`
				Daily   int `mapstructure:"daily"`
				Weekly  int `mapstructure:"weekly"`
				Monthly int `mapstructure:"monthly"`
			} `mapstructure:"retention"`
		} `mapstructure:"databases"`
		Volumes struct {
			Enabled        bool   `mapstructure:"enabled"`
			Interval       string `mapstructure:"interval"`
			Compression    string `mapstructure:"compression"`
			Timeout        string `mapstructure:"timeout"`
			MaxConcurrency int    `mapstructure:"max_concurrency"`
			HelperImage    string `mapstructure:"helper_image"`
			S3             struct {
				Bucket       string `mapstructure:"bucket"`
				Region       string `mapstructure:"region"`
				Prefix       string `mapstructure:"prefix"`
				Endpoint     string `mapstructure:"endpoint"`
				PathStyle    bool   `mapstructure:"path_style"`
				SSEAlgorithm string `mapstructure:"sse_algorithm"`
				SSEKMSKeyID  string `mapstructure:"sse_kms_key_id"`
			} `mapstructure:"s3"`
			Retention struct {
				Keep int `mapstructure:"keep"`
			} `mapstructure:"retention"`
		} `mapstructure:"volumes"`
	} `mapstructure:"backups"`

	Images struct {
		AllowedRegistries []string `mapstructure:"allowed_registries"`
		RequireDigest     bool     `mapstructure:"require_digest"`
		Prune             struct {
			Enabled  bool   `mapstructure:"enabled"`
			Schedule string `mapstructure:"schedule"`
			KeepLast int    `mapstructure:"keep_last"`
		} `mapstructure:"prune"`
	} `mapstructure:"images"`

	Containers struct {
		MemoryLimit     string  `mapstructure:"memory_limit"`     // e.g., "512MB", "1GB"
		CPULimit        float64 `mapstructure:"cpu_limit"`        // CPU cores, e.g., 1.0 = 1 core
		PidsLimit       int64   `mapstructure:"pids_limit"`       // e.g., 512
		SecurityProfile string  `mapstructure:"security_profile"` // compat or strict
	} `mapstructure:"containers"`

	NetworkIsolation struct {
		Enabled  bool   `mapstructure:"enabled"`
		Prefix   string `mapstructure:"network_prefix"`
		Internal bool   `mapstructure:"internal"`
	} `mapstructure:"network_isolation"`

	Telemetry telemetry.Config `mapstructure:"telemetry"`

	TLS struct {
		ACME struct {
			Enabled         bool   `mapstructure:"enabled"`
			Email           string `mapstructure:"email"`
			Challenge       string `mapstructure:"challenge"`
			ObtainBatchSize int    `mapstructure:"obtain_batch_size"`
		} `mapstructure:"acme"`
	} `mapstructure:"tls"`

	DNS struct {
		Resolvers          []string `mapstructure:"resolvers"`
		PropagationTimeout string   `mapstructure:"propagation_timeout"`
		PollingInterval    string   `mapstructure:"polling_interval"`
	} `mapstructure:"dns"`
}

func warnDeprecatedConfigKeys(v *viper.Viper, log zerowrap.Logger) {
	for _, key := range []string{"server.tls_enabled", "server.force_hsts"} {
		if v.IsSet(key) {
			log.Warn().Str("key", key).Msg("deprecated config key — Gordon now uses an internal CA with automatic TLS; remove this from your config")
		}
	}
}

// initConfig loads configuration from file.
func initConfig(configPath string) (*viper.Viper, Config, error) {
	v := viper.New()
	if err := loadConfig(v, configPath); err != nil {
		return nil, Config{}, fmt.Errorf("failed to load config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, Config{}, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	if err := validateEntrypointMigration(v, cfg); err != nil {
		return nil, Config{}, err
	}
	if err := validateRetiredAppConfig(v); err != nil {
		return nil, Config{}, err
	}
	if _, err := buildAppMountPolicies(cfg); err != nil {
		return nil, Config{}, err
	}
	if err := validateDeviceConfig(v, cfg); err != nil {
		return nil, Config{}, err
	}

	return v, cfg, nil
}

// retiredAppConfigKeys are pre-v3 application keys removed with the
// declarative-apps cutover. Presence fails boot/reload closed with a
// config-retired diagnostic naming the fix — never a silent migration.
// Keys that remain live installation-level settings are deliberately
// absent: top-level "services" still declares standalone L4 workloads
// (see config.services), so it must never be rejected here.
var retiredAppConfigKeys = []struct {
	key  string
	hint string
}{
	{"routes", "declare [[services.<name>.http]] in an app file, then 'gordon apps apply'"},
	{"attachments", "declare [services.<name>] + volumes; attachments are removed"},
	{"network_groups", "declare [[network.shared]] with ownership verification"},
	{"service_routes", "declare [[services.<name>.http]] instead"},
	{"auto", "feature removed; declare explicit interfaces"},
	{"auto_route", "feature removed; declare explicit interfaces"},
	{"auto_route_allowed_domains", "feature removed; declare explicit interfaces"},
	{"previews", "staging is an ordinary app file"},
	{"env", "remove the [env] section; declare [env] and [services.<name>.secrets] in app files"},
}

// validateRetiredAppConfig rejects obsolete application configuration
// BEFORE any app/runtime mutation, at startup and reload. InConfig
// (not IsSet) targets explicit file keys only — installation defaults
// registered via SetDefault must not trip the rejection.
func validateRetiredAppConfig(v *viper.Viper) error {
	diagnostics := make([]string, 0, len(retiredAppConfigKeys))
	for _, retired := range retiredAppConfigKeys {
		if v.InConfig(retired.key) {
			diagnostics = append(diagnostics, fmt.Sprintf("key %q was removed in v3; %s", retired.key, retired.hint))
		}
	}
	if len(diagnostics) > 0 {
		return fmt.Errorf("config-retired: %s", strings.Join(diagnostics, "; "))
	}
	return nil
}

func validateEntrypointMigration(v *viper.Viper, cfg Config) error {
	if len(cfg.EntryPoints) > 0 {
		return nil
	}

	legacyKeys := make([]string, 0, 2)
	for _, key := range []string{"server.port", "server.tls_port"} {
		if v.IsSet(key) {
			legacyKeys = append(legacyKeys, key)
		}
	}
	if len(legacyKeys) == 0 {
		return nil
	}

	return fmt.Errorf("legacy %s configuration requires at least one [entrypoints] entry; see docs/upgrading.md", strings.Join(legacyKeys, " or "))
}

// resolveLogFilePath returns the configured log file path or a default.
func resolveLogFilePath(cfg Config) string {
	if cfg.Logging.File.Path != "" {
		return cfg.Logging.File.Path
	}
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	return filepath.Join(dataDir, "logs", "gordon.log")
}

// resolveRuntimeConfig converts a server.runtime config value to a socket path.
// "auto" or "" means auto-detect.
// Named runtimes ("podman", "docker") are resolved to well-known socket paths.
// URI schemes (unix://) are stripped so callers receive a bare path.
func resolveRuntimeConfig(value string) string {
	if value == "" || value == "auto" {
		return ""
	}
	// Named runtimes: resolve to well-known socket paths.
	switch value {
	case "podman":
		// Check XDG_RUNTIME_DIR first (rootless Podman).
		if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
			candidate := filepath.Join(xdg, "podman", "podman.sock")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
		// Fallback to system-wide Podman socket.
		return "/run/podman/podman.sock"
	case "docker":
		return "/var/run/docker.sock"
	}
	// Explicit socket path — strip URI scheme if present.
	if socketPath, ok := strings.CutPrefix(value, "unix://"); ok {
		return socketPath
	}
	return value
}

func resolveDataDir(dataDir string) string {
	if dataDir == "" {
		return DefaultDataDir()
	}
	return dataDir
}

func resolveRegistryDomains(cfg Config) (string, []string) {
	registryDomain := cfg.Server.RegistryDomain
	if registryDomain == "" {
		registryDomain = cfg.Server.GordonDomain
	}
	return registryDomain, append([]string{}, cfg.Server.LegacyRegistryDomains...)
}

// loadConfig loads configuration from file and sets defaults.
func loadConfig(v *viper.Viper, configPath string) error {
	v.SetDefault("server.registry_port", 5000)
	v.SetDefault("server.legacy_registry_domains", []string{})
	v.SetDefault("server.tls_cert_file", "")
	v.SetDefault("server.tls_key_file", "")
	v.SetDefault("tls.acme.enabled", false)
	v.SetDefault("tls.acme.email", "")
	v.SetDefault("tls.acme.challenge", "auto")
	v.SetDefault("tls.acme.obtain_batch_size", 1)
	v.SetDefault("dns.resolvers", publictls.DefaultDNSResolvers)
	v.SetDefault("dns.propagation_timeout", "5m")
	v.SetDefault("dns.polling_interval", "5s")
	v.SetDefault("server.force_https_redirect", false)
	v.SetDefault("server.data_dir", DefaultDataDir())
	v.SetDefault("server.runtime", "auto")
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "console")
	v.SetDefault("logging.file.enabled", false)
	v.SetDefault("logging.file.max_size", 100)
	v.SetDefault("logging.file.max_backups", 3)
	v.SetDefault("logging.file.max_age", 28)
	v.SetDefault("logging.container_logs.enabled", true)
	v.SetDefault("logging.container_logs.dir", "")
	v.SetDefault("logging.container_logs.max_size", 100)
	v.SetDefault("logging.container_logs.max_backups", 3)
	v.SetDefault("logging.container_logs.max_age", 28)
	v.SetDefault("logging.access_log.enabled", false)
	v.SetDefault("logging.access_log.format", "json")
	v.SetDefault("logging.access_log.output", "stdout")
	v.SetDefault("logging.access_log.file_path", "")
	v.SetDefault("logging.access_log.max_size", 100)
	v.SetDefault("logging.access_log.max_backups", 3)
	v.SetDefault("logging.access_log.max_age", 28)
	v.SetDefault("logging.access_log.exclude_health_checks", true)
	v.SetDefault("logging.access_log.syslog_identifier", "gordon-access")
	v.SetDefault("auth.enabled", true)
	// Note: auth.type defaults to "token" (the only supported mode)
	v.SetDefault("auth.secrets_backend", "")
	v.SetDefault("auth.token_expiry", "720h")
	v.SetDefault("api.rate_limit.enabled", true)
	v.SetDefault("api.rate_limit.global_rps", 500)
	v.SetDefault("api.rate_limit.per_ip_rps", 50)
	v.SetDefault("api.rate_limit.burst", 100)
	v.SetDefault("auto_route.enabled", false)
	v.SetDefault("network_isolation.enabled", true)
	v.SetDefault("network_isolation.network_prefix", "gordon")
	v.SetDefault("network_isolation.internal", false)
	v.SetDefault("volumes.auto_create", true)
	v.SetDefault("volumes.prefix", "gordon")
	v.SetDefault("volumes.preserve", true)
	v.SetDefault("backups.databases.enabled", false)
	v.SetDefault("backups.databases.schedule", string(domain.ScheduleDaily))
	v.SetDefault("backups.databases.storage_dir", "")
	v.SetDefault("backups.databases.retention.hourly", 0)
	v.SetDefault("backups.databases.retention.daily", 0)
	v.SetDefault("backups.databases.retention.weekly", 0)
	v.SetDefault("backups.databases.retention.monthly", 0)
	v.SetDefault("backups.volumes.enabled", false)
	v.SetDefault("backups.volumes.interval", "24h")
	v.SetDefault("backups.volumes.compression", string(domain.VolumeBackupCompressionGzip))
	v.SetDefault("backups.volumes.timeout", "2h")
	v.SetDefault("backups.volumes.max_concurrency", 2)
	v.SetDefault("backups.volumes.helper_image", "alpine:3.20")
	v.SetDefault("backups.volumes.s3.bucket", "")
	v.SetDefault("backups.volumes.s3.region", "")
	v.SetDefault("backups.volumes.s3.prefix", "")
	v.SetDefault("backups.volumes.s3.endpoint", "")
	v.SetDefault("backups.volumes.s3.path_style", false)
	v.SetDefault("backups.volumes.s3.sse_algorithm", "")
	v.SetDefault("backups.volumes.s3.sse_kms_key_id", "")
	v.SetDefault("backups.volumes.retention.keep", 14)
	v.SetDefault("images.allowed_registries", []string{})
	v.SetDefault("images.require_digest", false)
	v.SetDefault("images.prune.enabled", false)
	v.SetDefault("images.prune.schedule", string(domain.ScheduleDaily))
	v.SetDefault("images.prune.keep_last", domain.DefaultImagePruneKeepLast)
	v.SetDefault("containers.security_profile", "compat")
	v.SetDefault("telemetry.enabled", false)
	v.SetDefault("telemetry.endpoint", "")
	v.SetDefault("telemetry.auth_token", "")
	v.SetDefault("telemetry.traces", true)
	v.SetDefault("telemetry.metrics", true)
	v.SetDefault("telemetry.logs", true)
	v.SetDefault("telemetry.trace_sample_rate", 1.0)

	v.SetDefault("server.max_concurrent_connections", -1) // -1 = use default (10000), 0 = no limit
	v.SetDefault("server.registry_allowed_ips", []string{})
	v.SetDefault("server.proxy_allowed_ips", []string{})
	v.SetDefault("server.registry_listen_address", "")

	ConfigureViper(v, configPath)

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("failed to read config file: %w", err)
		}
	}

	v.SetEnvPrefix("GORDON")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	return nil
}
