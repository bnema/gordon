// Package app provides the application initialization and wiring.
package app

import (
	"fmt"
	"strings"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/publictls"
)

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

	return v, cfg, nil
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
	v.SetDefault("env.dir", "") // defaults to {data_dir}/env when empty
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
	v.SetDefault("deploy.pull_policy", container.PullPolicyIfTagChanged)
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
	v.SetDefault("deploy.readiness_delay", "5s")
	v.SetDefault("deploy.readiness_mode", "auto")
	v.SetDefault("deploy.health_timeout", "90s")
	v.SetDefault("deploy.stabilization_delay", "2s")
	v.SetDefault("deploy.tcp_probe_timeout", "30s")
	v.SetDefault("deploy.http_probe_timeout", "60s")
	v.SetDefault("deploy.attachment_readiness_timeout", "30s")
	v.SetDefault("deploy.drain_mode", "auto")
	v.SetDefault("deploy.drain_timeout", "30s")

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
