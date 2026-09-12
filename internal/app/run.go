// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	zerowrapotel "github.com/bnema/zerowrap/otel"
	"github.com/spf13/viper"
	"golang.org/x/sys/unix"

	// Adapters - Output
	"github.com/bnema/gordon/internal/adapters/out/accesslog"
	acmelego "github.com/bnema/gordon/internal/adapters/out/acmelego"
	acmestore "github.com/bnema/gordon/internal/adapters/out/acmestore"
	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/adapters/out/envloader"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/adapters/out/httpprober"
	"github.com/bnema/gordon/internal/adapters/out/logwriter"
	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/gordon/internal/adapters/out/ratelimit"
	s3storage "github.com/bnema/gordon/internal/adapters/out/s3"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"

	// OTel
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	// Adapters - Input
	"github.com/bnema/gordon/internal/adapters/dto"
	acmehttp "github.com/bnema/gordon/internal/adapters/in/http/acme"
	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	authhandler "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/adapters/in/http/onboarding"
	proxyadapter "github.com/bnema/gordon/internal/adapters/in/http/proxy"
	"github.com/bnema/gordon/internal/adapters/in/http/registry"
	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"

	// Boundaries
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"

	// Domain
	"github.com/bnema/gordon/internal/domain"

	// Packages
	"github.com/bnema/gordon/pkg/version"

	// Use cases
	"github.com/bnema/gordon/internal/adapters/out/appsecrets"
	"github.com/bnema/gordon/internal/adapters/out/appstate"
	"github.com/bnema/gordon/internal/adapters/out/imageref"
	"github.com/bnema/gordon/internal/usecase/apps"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
	"github.com/bnema/gordon/internal/usecase/auth"
	"github.com/bnema/gordon/internal/usecase/backup"
	"github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	cronSvc "github.com/bnema/gordon/internal/usecase/cron"
	"github.com/bnema/gordon/internal/usecase/deployment"
	"github.com/bnema/gordon/internal/usecase/health"
	"github.com/bnema/gordon/internal/usecase/images"
	"github.com/bnema/gordon/internal/usecase/logs"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/gordon/internal/usecase/proxy"
	"github.com/bnema/gordon/internal/usecase/publictls"
	registrySvc "github.com/bnema/gordon/internal/usecase/registry"
	"github.com/bnema/gordon/internal/usecase/registrystate"
	secretsSvc "github.com/bnema/gordon/internal/usecase/secrets"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
	"github.com/bnema/gordon/internal/usecase/traffic"
	volumesSvc "github.com/bnema/gordon/internal/usecase/volumes"

	// Pkg
	"github.com/bnema/gordon/pkg/bytesize"
	"github.com/bnema/gordon/pkg/duration"
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

	Env struct {
		Dir string `mapstructure:"dir"`
	} `mapstructure:"env"`

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

// services holds all the services used by the application.
type services struct {
	runtime               *docker.Runtime
	eventBus              *eventbus.InMemory
	blobStorage           *filesystem.BlobStorage
	manifestStorage       *filesystem.ManifestStorage
	backupStorage         *filesystem.BackupStorage
	volumeBackupStore     out.VolumeBackupStorage
	volumeBackupCfg       domain.VolumeBackupConfig
	envLoader             out.EnvLoader
	logWriter             *logwriter.LogWriter
	tokenStore            out.TokenStore
	configSvc             *config.Service
	secretSvc             *secretsSvc.Service
	containerSvc          *container.Service
	backupSvc             *backup.Service
	volumeBackupSvc       *backup.VolumeService
	registrySvc           *registrySvc.Service
	healthSvc             *health.Service
	logSvc                *logs.Service
	imageSvc              *images.Service
	volumeSvc             *volumesSvc.Service
	proxySvc              *proxy.Service
	serviceSecretProvider out.SecretProvider
	authSvc               *auth.Service
	authHandler           *authhandler.Handler
	adminHandler          *admin.Handler
	httpProxyHandler      http.Handler
	httpsProxyHandler     http.Handler
	internalRegUser       string
	internalRegPass       string
	envDir                string
	maxBlobChunkSize      int64
	maxBlobSize           int64
	caAdapter             *pkiadapter.CA
	pkiSvc                *pkiusecase.Service
	appState              out.AppState
	// gcBarrier serializes resource acquisition and its durable
	// protection publication against destructive prune.
	gcBarrier            out.GCBarrier
	appDeploySvc         *deployment.Service
	appSvc               in.AppService
	appActivator         *apptraffic.Activator
	appHostIndex         *apptraffic.HostIndex
	appTrafficPublisher  *appTrafficPublisher
	appMonitor           *appMonitor
	reloadCoordinator    *reloadCoordinator
	publicTLSSvc         in.PublicTLSService
	publicTLSRuntime     publicTLSRuntime
	trafficManager       *trafficadapter.Manager
	tlsHTTPEntryPoints   map[string]struct{}
	smartHTTPEntryPoints map[string]struct{}
	registryHandler      interface {
		UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
	}
}

// Run initializes and starts the Gordon application.
func Run(ctx context.Context, configPath string) error {
	// Load configuration
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return err
	}

	// Initialize logger
	log, cleanup, err := initLogger(cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	ctx = zerowrap.WithCtx(ctx, log)

	// Initialize OpenTelemetry
	telProvider, telShutdown, err := telemetry.NewProvider(ctx, cfg.Telemetry, "gordon", version.Version())
	if err != nil {
		log.Warn().Err(err).Msg("failed to initialize telemetry, continuing without it")
	} else {
		// Use a fresh context for shutdown so a canceled app ctx doesn't prevent flushing.
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			telShutdown(shutdownCtx)
		}()
		if cfg.Telemetry.Enabled && cfg.Telemetry.Endpoint != "" {
			// Bridge zerowrap logs to OTel if log export is enabled
			if cfg.Telemetry.Logs && telProvider.LogProvider != nil {
				otelHook := zerowrapotel.NewHookWithProvider(telProvider.LogProvider, "gordon")
				log = zerowrap.WithHook(log, otelHook)
				ctx = zerowrap.WithCtx(ctx, log)
			}
			log.Info().Str("endpoint", cfg.Telemetry.Endpoint).Msg("telemetry initialized")
		}
	}

	log.Info().Msg("Gordon starting")

	warnDeprecatedConfigKeys(v, log)

	// Create PID file
	pidFile := createPidFile(log)
	if pidFile != "" {
		defer removePidFile(pidFile, log)
	}

	// Create all services
	svc, err := createServices(ctx, v, cfg, log)
	if err != nil {
		return err
	}

	// Register event handlers
	cleanupHandlers, err := registerEventHandlers(ctx, svc)
	if err != nil {
		return err
	}
	// cleanupHandlers is passed into runServers so it can stop debounce
	// timers before graceful shutdown, preventing deploys during drain.

	// Set up config hot reload
	if err := setupConfigHotReload(ctx, svc.configSvc, svc.reloadCoordinator); err != nil {
		return err
	}

	// Start event bus
	if err := svc.eventBus.Start(); err != nil {
		return log.WrapErr(err, "failed to start event bus")
	}
	defer svc.eventBus.Stop()

	// Start servers, wait for listeners to bind, then sync/auto-start containers.
	return runServers(ctx, v, cfg, svc, svc.reloadCoordinator, cleanupHandlers, log)
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

	return v, cfg, nil
}

// retiredAppConfigKeys are pre-v2.50 application keys removed with the
// declarative-apps cutover. Presence fails boot/reload closed with a
// config-retired diagnostic naming the fix — never a silent migration.
var retiredAppConfigKeys = []struct {
	key  string
	hint string
}{
	{"routes", "declare [[service.http]] in an app file, then 'gordon apps apply'"},
	{"attachments", "declare [[service]] + volumes; attachments are removed"},
	{"network_groups", "declare [[network.shared]] with ownership verification"},
	{"services", "one [[service]] per app file"},
	{"service_routes", "declare [[service.http]] instead"},
	{"auto", "feature removed; declare explicit interfaces"},
	{"auto_route", "feature removed; declare explicit interfaces"},
	{"auto_route_allowed_domains", "feature removed; declare explicit interfaces"},
	{"network_isolation", "installation network policy; per-app isolation is declared via [[network.shared]] in app files"},
	{"previews", "staging is an ordinary app file"},
}

// validateRetiredAppConfig rejects obsolete application configuration
// BEFORE any app/runtime mutation, at startup and reload. InConfig
// (not IsSet) targets explicit file keys only — installation defaults
// registered via SetDefault must not trip the rejection.
func validateRetiredAppConfig(v *viper.Viper) error {
	for _, retired := range retiredAppConfigKeys {
		if v.InConfig(retired.key) {
			return fmt.Errorf("config-retired: key %q was removed in v2.50; %s", retired.key, retired.hint)
		}
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

// initLogger initializes the zerowrap logger.
func initLogger(cfg Config) (zerowrap.Logger, func(), error) {
	logConfig := zerowrap.Config{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
	}

	// Always enable file logging so admin API can read process logs
	// (Config flag is respected for backward-compatibility)
	logPath := cfg.Logging.File.Path
	if logPath == "" {
		dataDir := cfg.Server.DataDir
		if dataDir == "" {
			dataDir = DefaultDataDir()
		}
		logPath = filepath.Join(dataDir, "logs", "gordon.log")
	}

	log, cleanup, err := zerowrap.NewWithFile(logConfig, zerowrap.FileConfig{
		Enabled:    cfg.Logging.File.Enabled,
		Path:       logPath,
		MaxSize:    cfg.Logging.File.MaxSize,
		MaxBackups: cfg.Logging.File.MaxBackups,
		MaxAge:     cfg.Logging.File.MaxAge,
		Compress:   true,
	})
	if err != nil {
		return zerowrap.Default(), nil, fmt.Errorf("failed to create logger with file: %w", err)
	}
	return log, cleanup, nil
}

// initAccessLog creates an access log writer when access logging is enabled.
// Returns nil, nil when disabled — callers must treat nil writer as "disabled".
func initAccessLog(cfg Config, log zerowrap.Logger) (*accesslog.Writer, error) {
	if !cfg.Logging.AccessLog.Enabled {
		return nil, nil
	}

	filePath := cfg.Logging.AccessLog.FilePath
	if filePath == "" && cfg.Logging.AccessLog.Output == "file" {
		dataDir := cfg.Server.DataDir
		if dataDir == "" {
			dataDir = DefaultDataDir()
		}
		filePath = filepath.Join(dataDir, "logs", "access.log")
	}

	writer, err := accesslog.New(accesslog.Config{
		Format:           cfg.Logging.AccessLog.Format,
		Output:           cfg.Logging.AccessLog.Output,
		FilePath:         filePath,
		MaxSize:          cfg.Logging.AccessLog.MaxSize,
		MaxBackups:       cfg.Logging.AccessLog.MaxBackups,
		MaxAge:           cfg.Logging.AccessLog.MaxAge,
		SyslogIdentifier: cfg.Logging.AccessLog.SyslogIdentifier,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize access log: %w", err)
	}

	log.Info().
		Str("format", cfg.Logging.AccessLog.Format).
		Str("output", cfg.Logging.AccessLog.Output).
		Msg("access log enabled")

	return writer, nil
}

// serviceInit holds the shared context for service initialization helpers.
type serviceInit struct {
	ctx context.Context
	v   *viper.Viper
	cfg Config
	log zerowrap.Logger
	svc *services
}

// createServices creates all the application services for server runtime.
// Public ACME reconciliation is started later, after the HTTP listener is bound.
func createServices(ctx context.Context, v *viper.Viper, cfg Config, log zerowrap.Logger) (_ *services, retErr error) {
	return createServicesWithOptions(ctx, v, cfg, log)
}

// createServicesWithOptions creates all the application services.
// ACME Reconcile and renewal loop are started later from runServers,
// after HTTP listeners are bound.
func createServicesWithOptions(ctx context.Context, v *viper.Viper, cfg Config, log zerowrap.Logger) (_ *services, retErr error) {
	si := &serviceInit{
		ctx: ctx,
		v:   v,
		cfg: cfg,
		log: log,
		svc: &services{},
	}
	defer func() {
		if retErr != nil {
			if si.svc.pkiSvc != nil {
				si.svc.pkiSvc.Stop()
			}
			if si.svc.publicTLSSvc != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := si.svc.publicTLSSvc.Stop(ctx); err != nil {
					si.log.Warn().Err(err).Msg("failed to stop public TLS service during createServices cleanup")
				}
			}
		}
	}()
	var err error

	// Create output adapters
	runtimeSocket := resolveRuntimeConfig(v.GetString("server.runtime"))
	if si.svc.runtime, si.svc.eventBus, err = createOutputAdapters(ctx, log, runtimeSocket); err != nil {
		return nil, err
	}

	// Create storage
	if si.svc.blobStorage, si.svc.manifestStorage, err = createStorage(cfg, log); err != nil {
		return nil, err
	}

	// Create log writer
	if si.svc.logWriter, err = createLogWriter(cfg, log); err != nil {
		return nil, err
	}

	// Create auth service (if enabled)
	if si.svc.tokenStore, si.svc.authSvc, err = createAuthService(ctx, cfg, log); err != nil {
		return nil, err
	}

	if err := setupInternalRegistryAuth(si.svc, log); err != nil {
		return nil, err
	}

	// Create config service
	si.svc.configSvc = config.NewService(v, si.svc.eventBus)
	if err := si.svc.configSvc.Load(ctx); err != nil {
		return nil, log.WrapErr(err, "failed to load configuration")
	}

	if err := si.initPKI(); err != nil {
		return nil, err
	}

	if err := si.initSecrets(); err != nil {
		return nil, err
	}

	if err := si.initPublicTLS(); err != nil {
		return nil, err
	}

	if err := si.initRuntimeProxyAndTraffic(); err != nil {
		return nil, err
	}

	si.svc.reloadCoordinator = newReloadCoordinator(v, si.svc.configSvc, si.svc.proxySvc, nil, si.svc.eventBus, si.svc.publicTLSSvc, log)
	si.registerReloadCoordinatorHooks()

	si.initHandlers()

	return si.svc, nil
}

// initPKI initialises the internal CA and PKI service when TLS is enabled.
func (si *serviceInit) initPKI() error {
	if !hasTLSCapableEntrypoint(si.cfg) {
		si.log.Info().Msg("internal CA disabled (no TLS-capable entrypoint configured)")
		return nil
	}

	if (si.cfg.Server.TLSCertFile == "") != (si.cfg.Server.TLSKeyFile == "") {
		return fmt.Errorf("both tls_cert_file and tls_key_file must be set, or neither")
	}

	caAdapter, err := pkiadapter.NewCA(resolveDataDir(si.cfg.Server.DataDir), si.log)
	if err != nil {
		return si.log.WrapErr(err, "failed to initialize internal CA")
	}
	si.svc.caAdapter = caAdapter
	si.svc.pkiSvc = pkiusecase.NewService(si.ctx, caAdapter, si.appRoutes(), []string{si.cfg.Server.GordonDomain}, si.log)
	return nil
}

// initPublicTLS initializes the public ACME TLS service if enabled.
func (si *serviceInit) initPublicTLS() error {
	if !si.cfg.TLS.ACME.Enabled {
		return nil
	}

	if err := validatePublicTLSReadiness(si.cfg); err != nil {
		return err
	}

	ctx := si.ctx
	log := si.log

	dnsCfg, err := buildDNSConfig(si.cfg)
	if err != nil {
		return log.WrapErr(err, "invalid DNS configuration")
	}

	publicTLSCfg := publictls.Config{
		Enabled:         si.cfg.TLS.ACME.Enabled,
		Email:           si.cfg.TLS.ACME.Email,
		Challenge:       si.cfg.TLS.ACME.Challenge,
		HTTPPort:        effectiveHTTP01Port(si.cfg),
		TLSPort:         effectivePublicTLSPort(si.cfg),
		DataDir:         resolveDataDir(si.cfg.Server.DataDir),
		ObtainBatchSize: si.cfg.TLS.ACME.ObtainBatchSize,
		DNS:             dnsCfg,
	}

	tokenResolver := secrets.NewPublicTLSResolver(secrets.PublicTLSResolverConfig{})

	effective, err := publictls.ResolveEffectiveChallenge(ctx, publicTLSCfg, tokenResolver)
	if err != nil {
		return log.WrapErr(err, "resolve ACME challenge")
	}
	if err := validateEffectivePublicTLSReadiness(si.cfg, effective); err != nil {
		return err
	}

	store, err := acmestore.New(filepath.Join(resolveDataDir(si.cfg.Server.DataDir), "acme"))
	if err != nil {
		return log.WrapErr(err, "create ACME store")
	}

	challenges := publictls.NewHTTP01Challenges()

	var zoneResolver *acmelego.CloudflareZoneResolver
	if effective.Mode == domain.ACMEChallengeCloudflareDNS01 {
		zoneResolver = acmelego.NewCloudflareZoneResolver(effective.Token)
	}

	issuer, err := acmelego.NewIssuer(acmelego.Config{
		Email:                 si.cfg.TLS.ACME.Email,
		Challenge:             effective.Mode,
		Token:                 effective.Token,
		Store:                 store,
		HTTPChallengeSink:     challenges,
		DNSResolvers:          publicTLSCfg.DNS.Resolvers,
		DNSPropagationTimeout: publicTLSCfg.DNS.PropagationTimeout,
		DNSPollingInterval:    publicTLSCfg.DNS.PollingInterval,
	})
	if err != nil {
		return log.WrapErr(err, "create ACME issuer")
	}

	svc := publictls.NewService(publicTLSCfg, publictls.ServiceDeps{
		Routes:          si.appRoutes(),
		Issuer:          issuer,
		Store:           store,
		ZoneResolver:    zoneResolver,
		Challenges:      challenges,
		Effective:       effective,
		AdditionalHosts: []string{si.cfg.Server.GordonDomain},
	})

	if err := svc.Load(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to load ACME certificates, continuing")
	}

	log.Info().
		Str("email", si.cfg.TLS.ACME.Email).
		Str("challenge", string(effective.Mode)).
		Msg("public ACME TLS initialized (runtime start deferred)")

	si.svc.publicTLSSvc = svc
	si.svc.publicTLSRuntime = svc
	return nil
}

// publicTLSRuntime is the subset of in.PublicTLSService needed at server runtime
// after the HTTP listener is bound: reconcile missing certs and start the renewal
// loop. It is nil when ACME is disabled.
type publicTLSRuntime interface {
	Reconcile(context.Context) error
	StartRenewalLoop(context.Context, time.Duration) <-chan struct{}
}

func startPublicTLSRuntime(ctx context.Context, svc publicTLSRuntime, log zerowrap.Logger) error {
	if svc == nil {
		return nil
	}
	reconcileErr := svc.Reconcile(ctx)
	svc.StartRenewalLoop(ctx, time.Hour)
	log.Info().Msg("public ACME TLS runtime started")
	return reconcileErr
}

// initSecrets creates the domain secret store, env loader, and secret service.
func (si *serviceInit) initSecrets() error {
	envDir, backend, passStore, domainSecretStore, err := createDomainSecretStore(si.cfg, si.log)
	if err != nil {
		return err
	}
	si.svc.envDir = envDir

	if si.svc.envLoader, err = createEnvLoader(backend, envDir, passStore, si.log); err != nil {
		return err
	}

	si.svc.serviceSecretProvider = createStandaloneServiceSecretProvider(backend, resolveDataDir(si.cfg.Server.DataDir), si.log)
	si.svc.secretSvc = secretsSvc.NewService(domainSecretStore, si.log, si.svc.eventBus)
	return nil
}

// initRuntimeAndProxy creates container, backup, registry, image, volume, and proxy services.
func (si *serviceInit) initRuntimeProxyAndTraffic() error {
	if err := si.initRuntimeAndProxy(); err != nil {
		return err
	}
	si.svc.trafficManager = trafficadapter.NewManager()
	return si.initApps()
}

func containerResourceLimits(cfg Config) (deployment.ResourceLimits, error) {
	limits := deployment.ResourceLimits{PidsLimit: cfg.Containers.PidsLimit}
	if cfg.Containers.MemoryLimit != "" {
		parsed, err := bytesize.Parse(cfg.Containers.MemoryLimit)
		if err != nil {
			return limits, fmt.Errorf("invalid containers.memory_limit %q: %w", cfg.Containers.MemoryLimit, err)
		}
		limits.MemoryBytes = parsed
	}
	if cfg.Containers.CPULimit > 0 {
		limits.NanoCPUs = int64(cfg.Containers.CPULimit * float64(time.Second))
	}
	return limits, nil
}

// initApps wires the single v2.50 app engine: bbolt state, image digests,
// pass-backed secrets, deployment/lifecycle execution, and traffic
// activation. The daemon is the sole app-state writer; CLI reaches the
// engine only through the admin /apps surface.
func (si *serviceInit) initApps() error {
	dataDir := resolveDataDir(si.cfg.Server.DataDir)
	store, err := appstate.NewStore(dataDir, si.log)
	if err != nil {
		return si.log.WrapErr(err, "failed to open app state")
	}
	store.WithRevisionRetention(si.cfg.Apps.RevisionRetention)
	si.svc.appState = store
	// One process-wide GC barrier owns the ordering between resource
	// acquisition/publication and destructive prune.
	si.svc.gcBarrier = newGCBarrier()
	if si.svc.backupSvc != nil {
		si.svc.backupSvc.WithAppState(store)
	}
	if si.svc.volumeBackupSvc != nil {
		si.svc.volumeBackupSvc.WithAppState(store)
	}
	if si.svc.imageSvc != nil {
		si.svc.imageSvc.WithPrunePorts(store, si.svc.runtime, si.svc.gcBarrier)
	}
	if si.svc.volumeSvc != nil {
		si.svc.volumeSvc.WithPrunePorts(store, si.svc.runtime, si.svc.gcBarrier)
	}
	registryDomain := si.svc.configSvc.GetRegistryDomain()
	imagePolicy := domain.ImageSourcePolicy{
		AllowedRegistries:    si.cfg.Images.AllowedRegistries,
		RequireDigest:        si.cfg.Images.RequireDigest,
		InstallationRegistry: registryDomain,
	}
	if err := imagePolicy.Validate(); err != nil {
		return fmt.Errorf("invalid image registry policy: %w", err)
	}
	resolver := imageref.NewResolver(registryDomain, si.svc.manifestStorage, nil).WithPolicy(imagePolicy)
	// The publisher is the single serialized HTTP/L4 boundary: raw
	// activations, fail-closed recovery withdrawal, and reload all go
	// through it. It resolves writers/permission paths late-bound.
	si.svc.appTrafficPublisher = newAppTrafficPublisher(si.svc, si.cfg)
	limits, err := containerResourceLimits(si.cfg)
	if err != nil {
		return err
	}
	si.svc.appDeploySvc = deployment.NewService(deployment.Deps{
		State:   store,
		Runtime: si.svc.runtime,
		Images:  resolver,
		Secrets: si.svc.serviceSecretProvider,
		Registry: deployment.RegistryConfig{
			Domain:      registryDomain,
			PullAddress: net.JoinHostPort("127.0.0.1", strconv.Itoa(si.svc.configSvc.GetRegistryPort())),
			Username:    si.svc.internalRegUser,
			Password:    si.svc.internalRegPass,
		},
		Networks: deployment.NetworkConfig{
			Prefix:   si.cfg.NetworkIsolation.Prefix,
			Internal: si.cfg.NetworkIsolation.Internal,
		},
		Limits:      limits,
		ImagePolicy: imagePolicy,
		Traffic:     si.svc.appTrafficPublisher,
	}, si.log).WithGCBarrier(si.svc.gcBarrier)
	si.svc.appActivator = apptraffic.NewActivator(si.log)
	si.svc.appSvc = apps.NewAppServiceImpl(store, si.svc.appDeploySvc, appsecrets.NewStore(si.log), si.log).
		WithEntrypoints(appEntrypointListeners(si.cfg)).
		WithGCBarrier(si.svc.gcBarrier).
		WithImagePolicy(imagePolicy)
	// Health checks resolve from ACTIVE state (loopback backends).
	si.svc.healthSvc = health.NewService(store, si.svc.runtime, httpprober.New(), si.log)
	// ACTIVE-derived host index for the proxy: rebuilt after every
	// activation and at boot. Wired here (store is open); the proxy
	// itself was built earlier in initRuntimeAndProxy.
	si.svc.appHostIndex = apptraffic.NewHostIndex()
	if si.svc.proxySvc != nil {
		si.svc.proxySvc.WithAppTargets(si.svc.appHostIndex)
	}
	return nil
}

// appRouteSource adapts the ACTIVE-derived host index plus installation
// external routes to the route-source interfaces (PKI AppRoutes,
// public-TLS RouteSource). bbolt stays authoritative; this adapter only
// projects. Fail-closed: unwired index yields zero app hosts. The index
// is late-bound (built after PKI/public-TLS), so it resolves per call.
type appRouteSource struct {
	index    func() *apptraffic.HostIndex
	external func() map[string]string
}

// AppHosts implements out.AppHostSource.
func (s *appRouteSource) AppHosts() []out.AppHost {
	if s == nil || s.index == nil {
		return nil
	}
	index := s.index()
	if index == nil {
		return nil
	}
	return index.AppHosts()
}

// GetExternalRoutes implements the installation half of out.AppRoutes
// and the public-TLS RouteSource.
func (s *appRouteSource) GetExternalRoutes() map[string]string {
	if s == nil || s.external == nil {
		return nil
	}
	return s.external()
}

// GetRoutes implements the public-TLS RouteSource as a consumer-local
// adapter: host-only entries, Image explicitly unused (no app meaning).
func (s *appRouteSource) GetRoutes(context.Context) []domain.Route {
	hosts := s.AppHosts()
	routes := make([]domain.Route, 0, len(hosts))
	for _, h := range hosts {
		routes = append(routes, domain.Route{Domain: h.Host})
	}
	return routes
}

// appRoutes returns the shared ACTIVE-derived route source, late-bound
// to the host index built in initApps.
func (si *serviceInit) appRoutes() *appRouteSource {
	return &appRouteSource{
		index:    func() *apptraffic.HostIndex { return si.svc.appHostIndex },
		external: func() map[string]string { return si.svc.configSvc.GetExternalRoutes() },
	}
}

// appEntrypointPolicies maps installation entrypoints to the projection
// policy (trusted CIDRs, raw-fallback behavior, transport limits).
func appEntrypointPolicies(cfg Config) map[string]apptraffic.EntrypointPolicy {
	policies := make(map[string]apptraffic.EntrypointPolicy, len(cfg.EntryPoints))
	for name, entry := range cfg.EntryPoints {
		policies[name] = apptraffic.EntrypointPolicy{
			Name:                    name,
			Address:                 entry.Address,
			Protocol:                entry.Protocol,
			TrustedCIDRs:            entry.TrustedCIDRs,
			RawFallback:             entry.RawFallback,
			RawFallbackTrustedCIDRs: entry.RawFallbackTrustedCIDRs,
			AllowPublicRawFallback:  entry.AllowPublicRawFallback,
		}
	}
	return policies
}

// appEntrypointListeners maps installation entrypoints to the canonical
// listener an app L4 publish declaration must match exactly.
func appEntrypointListeners(cfg Config) map[string]domain.EntryPointListener {
	listeners := make(map[string]domain.EntryPointListener, len(cfg.EntryPoints))
	for name, entry := range cfg.EntryPoints {
		listeners[name] = domain.EntryPointListener{Address: entry.Address, Protocol: entry.Protocol}
	}
	return listeners
}

// rebuildAppHostIndex re-projects ACTIVE state into the proxy host index
// and applies the full HTTP/L4 traffic graph through the serialized
// publisher. Call after every activation (deploy/start/restart/stop/
// remove) and at boot; callers decide whether a failure is fatal to the
// mutation.
func rebuildAppHostIndex(ctx context.Context, svc *services) error {
	if svc.appTrafficPublisher == nil {
		return nil
	}
	return svc.appTrafficPublisher.RebuildTraffic(ctx)
}

func (si *serviceInit) initRuntimeAndProxy() error {
	var err error

	if si.svc.containerSvc, err = createContainerService(si.ctx, si.v, si.cfg, si.svc, si.log); err != nil {
		return err
	}

	if si.svc.backupStorage, si.svc.backupSvc, err = createBackupService(si.cfg, si.svc, si.log); err != nil {
		return err
	}
	if si.svc.volumeBackupStore, si.svc.volumeBackupSvc, si.svc.volumeBackupCfg, err = createVolumeBackupService(si.ctx, si.cfg, si.svc, si.log); err != nil {
		return err
	}

	registryState := registrystate.New()
	si.svc.registrySvc = registrySvc.NewService(si.svc.blobStorage, si.svc.manifestStorage, si.svc.eventBus, registryState)
	si.svc.imageSvc = images.NewService(si.svc.runtime, si.svc.manifestStorage, si.svc.blobStorage, si.log, registryState)
	si.svc.volumeSvc = volumesSvc.NewService(si.svc.runtime)

	injectTelemetryMetrics(si.cfg, si.svc, si.log)

	proxyCfg, err := buildProxyConfig(si.cfg, si.log)
	if err != nil {
		return err
	}
	si.svc.maxBlobChunkSize = proxyCfg.maxBlobChunkSize
	si.svc.maxBlobSize = proxyCfg.maxBlobSize
	si.svc.proxySvc = proxy.NewService(si.svc.configSvc, proxyCfg.proxyConfig)

	// Wire synchronous proxy cache invalidation for zero-downtime deployments.
	return nil
}

// initHandlers creates the auth, health, log, preview, and admin handlers.
func (si *serviceInit) registerReloadCoordinatorHooks() {
	if si.svc.reloadCoordinator == nil || si.svc.containerSvc == nil {
		return
	}

	si.svc.reloadCoordinator.SetContainerConfigApplier(func(reloadCtx context.Context, reloadCfg Config) error {
		containerCfg, err := buildContainerServiceConfig(reloadCtx, si.v, reloadCfg, si.svc, si.log)
		if err != nil {
			return err
		}
		managementHosts := []string{reloadCfg.Server.GordonDomain}
		if si.svc.pkiSvc != nil {
			si.svc.pkiSvc.SetAdditionalDomains(managementHosts)
		}
		if si.svc.publicTLSSvc != nil {
			si.svc.publicTLSSvc.SetAdditionalHosts(reloadCtx, managementHosts)
		}
		var tlsConfig *tls.Config
		if hasTLSCapableEntrypoint(reloadCfg) && si.svc.httpsProxyHandler != nil {
			var tlsErr error
			tlsConfig, tlsErr = proxyTLSConfig(reloadCfg, si.svc.pkiSvc, si.svc.publicTLSSvc, si.log)
			if tlsErr != nil {
				return tlsErr
			}
		}
		if err := si.svc.appTrafficPublisher.RebuildWithConfig(reloadCtx, reloadCfg); err != nil {
			return err
		}
		si.svc.tlsHTTPEntryPoints = registerTLSMuxHTTPServers(si.svc.trafficManager, reloadCfg, si.svc.httpsProxyHandler, tlsConfig, si.svc.tlsHTTPEntryPoints)
		si.svc.smartHTTPEntryPoints = registerSmartTCPHTTPServers(si.svc.trafficManager, reloadCfg, si.svc.httpProxyHandler, si.svc.httpsProxyHandler, tlsConfig, si.svc.smartHTTPEntryPoints)
		si.svc.containerSvc.UpdateConfig(containerCfg)
		return nil
	})
}

func (si *serviceInit) initHandlers() {
	if si.svc.authSvc != nil {
		internalAuth := authhandler.InternalAuth{
			Username: si.svc.internalRegUser,
			Password: si.svc.internalRegPass,
		}
		si.svc.authHandler = authhandler.NewHandler(si.svc.authSvc, internalAuth, si.log)
	}

	si.svc.logSvc = logs.NewService(resolveLogFilePath(si.cfg), si.cfg.Logging.File.Enabled, si.svc.runtime, si.log)
	// initApps (via initRuntimeProxyAndTraffic) runs before initHandlers,
	// so durable ACTIVE state is already open here.
	if si.svc.appState != nil {
		si.svc.logSvc.WithAppState(si.svc.appState)
	}

	if si.svc.trafficManager == nil {
		si.svc.trafficManager = trafficadapter.NewManager()
	}

	si.svc.adminHandler = admin.NewHandler(admin.HandlerDeps{
		ConfigSvc:       si.svc.configSvc,
		AuthSvc:         si.svc.authSvc,
		ContainerSvc:    si.svc.containerSvc,
		HealthSvc:       si.svc.healthSvc,
		SecretSvc:       si.svc.secretSvc,
		LogSvc:          si.svc.logSvc,
		RegistrySvc:     si.svc.registrySvc,
		ReloadTrigger:   si.svc.reloadCoordinator,
		Log:             si.log,
		BackupSvc:       si.svc.backupSvc,
		VolumeBackupSvc: si.svc.volumeBackupSvc,
		ImageSvc:        si.svc.imageSvc,
		VolumeSvc:       si.svc.volumeSvc,
		PublicTLSSvc:    si.svc.publicTLSSvc,
		TrafficSvc:      si.svc.trafficManager,
		AppSvc:          si.svc.appSvc,
	})
}

// injectTelemetryMetrics creates and injects OTel metrics into services when
// telemetry is enabled. Skipped otherwise to avoid unnecessary allocations.
func injectTelemetryMetrics(cfg Config, svc *services, log zerowrap.Logger) {
	if !cfg.Telemetry.Enabled || !cfg.Telemetry.Metrics {
		return
	}
	gordonMetrics, err := telemetry.NewMetrics()
	if err != nil {
		log.Warn().Err(err).Msg("failed to create telemetry metrics, continuing without metrics")
		return
	}
	svc.registrySvc.SetMetrics(gordonMetrics)
	svc.eventBus.SetMetrics(gordonMetrics)
}

func setupInternalRegistryAuth(svc *services, log zerowrap.Logger) error {
	var err error
	svc.internalRegUser, svc.internalRegPass, err = generateInternalRegistryAuth()
	if err != nil {
		return log.WrapErr(err, "failed to generate internal registry credentials")
	}

	// Persist credentials to file for CLI access (gordon auth internal)
	if err := persistInternalCredentials(svc.internalRegUser, svc.internalRegPass); err != nil {
		log.Warn().Err(err).Msg("failed to persist internal credentials for CLI access")
	}

	log.Debug().Msg("internal registry auth generated for loopback pulls")
	return nil
}

func createDomainSecretStore(cfg Config, log zerowrap.Logger) (string, domain.SecretsBackend, *domainsecrets.PassStore, out.DomainSecretStore, error) {
	envDir := resolveEnvDir(cfg)
	backend, err := resolveSecretsBackend(cfg.Auth.SecretsBackend)
	if err != nil {
		return "", "", nil, nil, log.WrapErr(err, "failed to resolve secrets backend")
	}

	switch backend {
	case domain.SecretsBackendPass:
		passStore, err := domainsecrets.NewPassStore(log)
		if err != nil {
			return "", backend, nil, nil, log.WrapErr(err, "failed to create pass domain secret store")
		}
		if err := migrateEnvFilesToPass(envDir, passStore, log); err != nil {
			return "", backend, nil, nil, log.WrapErr(err, "failed to migrate env files to pass")
		}
		return envDir, backend, passStore, passStore, nil
	default:
		store, err := domainsecrets.NewFileStore(envDir, log)
		if err != nil {
			return "", backend, nil, nil, log.WrapErr(err, "failed to create domain secret store")
		}
		return envDir, backend, nil, store, nil
	}
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

// createOutputAdapters creates the container runtime and event bus.
func createOutputAdapters(ctx context.Context, log zerowrap.Logger, runtimeSocket string) (*docker.Runtime, *eventbus.InMemory, error) {
	detection := docker.DetectRuntimeSocket(runtimeSocket)

	var runtime *docker.Runtime
	var err error

	switch detection.Source {
	case "none":
		return nil, nil, fmt.Errorf("no container runtime found: checked Docker socket, Podman socket, DOCKER_HOST env var. Install Docker or Podman, or set server.runtime in config")
	case "DOCKER_HOST_passthrough":
		detection.RuntimeName = "docker"
		runtime, err = docker.NewRuntime()
	default:
		if detection.SocketPath != "" {
			runtime, err = docker.NewRuntimeWithSocket(detection.SocketPath)
		} else {
			runtime, err = docker.NewRuntime()
		}
	}
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to create container runtime")
	}

	if err := runtime.Ping(ctx); err != nil {
		return nil, nil, log.WrapErr(err, fmt.Sprintf("container runtime not available (detected: %s via %s)", detection.RuntimeName, detection.Source))
	}

	runtimeVersion, _ := runtime.Version(ctx)
	log.Info().
		Str("runtime", detection.RuntimeName).
		Str("version", runtimeVersion).
		Str("source", detection.Source).
		Msg("container runtime initialized")

	eventBus := eventbus.NewInMemory(100, log)

	return runtime, eventBus, nil
}

// createStorage creates blob and manifest storage.
func createStorage(cfg Config, log zerowrap.Logger) (*filesystem.BlobStorage, *filesystem.ManifestStorage, error) {
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}

	registryDir := filepath.Join(dataDir, "registry")

	blobStorage, err := filesystem.NewBlobStorage(registryDir, log)
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to create blob storage")
	}

	manifestStorage, err := filesystem.NewManifestStorage(registryDir, log)
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to create manifest storage")
	}

	return blobStorage, manifestStorage, nil
}

// createEnvLoader creates the environment loader with secret providers.
func createEnvLoader(backend domain.SecretsBackend, envDir string, passStore *domainsecrets.PassStore, log zerowrap.Logger) (out.EnvLoader, error) {
	switch backend {
	case domain.SecretsBackendPass:
		loader, err := envloader.NewPassLoader(passStore, log)
		if err != nil {
			return nil, log.WrapErr(err, "failed to create pass env loader")
		}
		return loader, nil
	default:
		loader, err := envloader.NewFileLoader(envDir, log)
		if err != nil {
			return nil, log.WrapErr(err, "failed to create env loader")
		}

		// Register secret providers
		passProvider := secrets.NewPassProvider(log)
		if passProvider.IsAvailable() {
			loader.RegisterSecretProvider(passProvider)
			log.Debug().Msg("pass secret provider registered")
		}

		sopsProvider := secrets.NewSopsProvider(log)
		if sopsProvider.IsAvailable() {
			loader.RegisterSecretProvider(sopsProvider)
			log.Debug().Msg("sops secret provider registered")
		}

		return loader, nil
	}
}

// createLogWriter creates the container log writer.
func createLogWriter(cfg Config, log zerowrap.Logger) (*logwriter.LogWriter, error) {
	if !cfg.Logging.ContainerLogs.Enabled {
		log.Debug().Msg("container log collection disabled")
		return nil, nil
	}

	// Determine log directory
	logDir := cfg.Logging.ContainerLogs.Dir
	if logDir == "" {
		dataDir := cfg.Server.DataDir
		if dataDir == "" {
			dataDir = DefaultDataDir()
		}
		logDir = filepath.Join(dataDir, "logs", "containers")
	}

	writer, err := logwriter.New(logwriter.Config{
		Dir:        logDir,
		MaxSize:    cfg.Logging.ContainerLogs.MaxSize,
		MaxBackups: cfg.Logging.ContainerLogs.MaxBackups,
		MaxAge:     cfg.Logging.ContainerLogs.MaxAge,
	})
	if err != nil {
		return nil, log.WrapErr(err, "failed to create container log writer")
	}

	log.Info().Str("dir", logDir).Msg("container log collection enabled")
	return writer, nil
}

const (
	internalRegistryUsername = "gordon-internal"
	serviceTokenSubject      = "gordon-service"
	serviceTokenDefaultTTL   = 30 * 24 * time.Hour
)

func generateInternalRegistryAuth() (string, string, error) {
	password, err := randomTokenHex(32)
	if err != nil {
		return "", "", err
	}
	return internalRegistryUsername, password, nil
}

func randomTokenHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// InternalCredentials holds the internal registry credentials for CLI access.
type InternalCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// getSecureRuntimeDir returns a secure directory for runtime files.
// Priority: XDG_RUNTIME_DIR > ~/.gordon/run
func getSecureRuntimeDir() (string, error) {
	// Try XDG_RUNTIME_DIR first (typically /run/user/<uid> on Linux)
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		gordonDir := filepath.Join(runtimeDir, "gordon")
		if err := os.MkdirAll(gordonDir, 0700); err == nil {
			return gordonDir, nil
		}
	}

	// Fall back to ~/.gordon/run
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}

	gordonDir := filepath.Join(homeDir, ".gordon", "run")
	if err := os.MkdirAll(gordonDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create runtime directory: %w", err)
	}

	return gordonDir, nil
}

// getInternalCredentialsFile returns the path to the internal credentials file.
// SECURITY: Credentials are stored in a secure location with restricted permissions.
func getInternalCredentialsFile() string {
	runtimeDir, err := getSecureRuntimeDir()
	if err != nil {
		// Fall back to temp dir if we can't get secure dir (shouldn't happen)
		return filepath.Join(os.TempDir(), "gordon-internal-creds.json")
	}
	return filepath.Join(runtimeDir, "internal-creds.json")
}

// persistInternalCredentials saves the internal registry credentials to a secure file.
// SECURITY: Credentials are stored in XDG_RUNTIME_DIR or ~/.gordon/run with 0600 permissions.
// The file is cleaned up on graceful shutdown but may persist if Gordon crashes.
// These credentials are for internal loopback communication only and are regenerated on each start.
func persistInternalCredentials(username, password string) error {
	creds := InternalCredentials{
		Username: username,
		Password: password,
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	credFile := getInternalCredentialsFile()

	// Ensure parent directory exists with secure permissions
	if err := os.MkdirAll(filepath.Dir(credFile), 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	// Write file with restrictive permissions (owner read/write only)
	if err := os.WriteFile(credFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}
	return nil
}

// cleanupInternalCredentials removes the internal credentials file.
func cleanupInternalCredentials() {
	_ = os.Remove(getInternalCredentialsFile())
}

// getInternalCredentialsCandidates returns candidate file paths in priority order:
// 1. XDG_RUNTIME_DIR/gordon/ (set by systemd for the daemon)
// 2. /run/user/<uid>/gordon/ (well-known systemd default, for CLI in shells without XDG_RUNTIME_DIR)
// 3. ~/.gordon/run/ (fallback for non-systemd environments)
// 4. os.TempDir() (last resort, matches getInternalCredentialsFile fallback path)
func getInternalCredentialsCandidates() []string {
	var candidates []string

	// 1. XDG_RUNTIME_DIR (set in daemon's environment)
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		candidates = append(candidates, filepath.Join(runtimeDir, "gordon", "internal-creds.json"))
	}

	// 2. /run/user/<uid>/gordon/ (systemd default, may not be in CLI's env)
	uid := os.Getuid()
	sysRuntime := filepath.Join("/run/user", fmt.Sprintf("%d", uid), "gordon", "internal-creds.json")
	// Avoid duplicate if XDG_RUNTIME_DIR already points here
	if len(candidates) == 0 || candidates[0] != sysRuntime {
		candidates = append(candidates, sysRuntime)
	}

	// 3. ~/.gordon/run/ fallback
	if homeDir, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(homeDir, ".gordon", "run", "internal-creds.json"))
	}

	// 4. os.TempDir() last resort — matches the fallback path in getInternalCredentialsFile,
	// ensuring GetInternalCredentials can find credentials even when getSecureRuntimeDir fails.
	candidates = append(candidates, filepath.Join(os.TempDir(), "gordon-internal-creds.json"))

	return candidates
}

// GetInternalCredentialsFromCandidates reads credentials from the first candidate file that exists.
// Exported for testing.
func GetInternalCredentialsFromCandidates(candidates []string) (*InternalCredentials, error) {
	var lastErr error
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			// Non-permission errors (e.g. EACCES) may be transient or path-specific;
			// record and try the next candidate rather than failing immediately.
			lastErr = fmt.Errorf("failed to read credentials file %s: %w", path, err)
			continue
		}
		var creds InternalCredentials
		if err := json.Unmarshal(data, &creds); err != nil {
			// Corrupt file — record and fall through to lower-priority candidates.
			lastErr = fmt.Errorf("failed to parse credentials at %s: %w", path, err)
			continue
		}
		return &creds, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no credentials file found (is Gordon running?): checked %v", candidates)
}

// GetInternalCredentials reads the internal registry credentials from file.
// Probes all candidate runtime directories so CLI works regardless of whether
// XDG_RUNTIME_DIR is set in the current shell environment.
func GetInternalCredentials() (*InternalCredentials, error) {
	return GetInternalCredentialsFromCandidates(getInternalCredentialsCandidates())
}

// createAuthService creates the authentication service and token store.
func createAuthService(ctx context.Context, cfg Config, log zerowrap.Logger) (out.TokenStore, *auth.Service, error) {
	if !cfg.Auth.Enabled {
		log.Warn().Msg("auth.enabled=false detected: running in local-only mode (registry loopback-only, admin API disabled)")
		return nil, nil, nil
	}

	authType, err := resolveAuthType(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve auth type: %w", err)
	}
	backend, err := resolveSecretsBackend(cfg.Auth.SecretsBackend)
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to resolve secrets backend")
	}
	dataDir := resolveDataDir(cfg.Server.DataDir)

	store, err := createTokenStore(backend, dataDir, log)
	if err != nil {
		return nil, nil, err
	}

	authConfig, err := buildAuthConfig(ctx, cfg, authType, backend, dataDir, log)
	if err != nil {
		return nil, nil, err
	}

	authSvc := auth.NewService(authConfig, store, log)

	log.Info().
		Str("type", string(authType)).
		Str("backend", string(backend)).
		Msg("registry authentication enabled")

	return store, authSvc, nil
}

// resolveAuthType determines the auth type from config.
// Token-only authentication is the only supported mode.
func resolveAuthType(cfg Config) (domain.AuthType, error) {
	if cfg.Auth.Type != "" && cfg.Auth.Type != "token" {
		return "", fmt.Errorf("unsupported auth.type %q; only \"token\" is supported", cfg.Auth.Type)
	}
	return domain.AuthTypeToken, nil
}

func resolveSecretsBackend(backend string) (domain.SecretsBackend, error) {
	switch backend {
	case "pass":
		return domain.SecretsBackendPass, nil
	case "sops":
		return domain.SecretsBackendSops, nil
	case "unsafe":
		return domain.SecretsBackendUnsafe, nil
	case "":
		return "", fmt.Errorf("auth.secrets_backend is required")
	default:
		return "", fmt.Errorf("unsupported auth.secrets_backend %q", backend)
	}
}

func resolveDataDir(dataDir string) string {
	if dataDir == "" {
		return DefaultDataDir()
	}
	return dataDir
}

func resolveEnvDir(cfg Config) string {
	dataDir := resolveDataDir(cfg.Server.DataDir)
	envDir := cfg.Env.Dir
	if envDir == "" {
		envDir = filepath.Join(dataDir, "env")
	}
	return envDir
}

func resolveRegistryDomains(cfg Config) (string, []string) {
	registryDomain := cfg.Server.RegistryDomain
	if registryDomain == "" {
		registryDomain = cfg.Server.GordonDomain
	}
	return registryDomain, append([]string{}, cfg.Server.LegacyRegistryDomains...)
}

func createTokenStore(backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) (out.TokenStore, error) {
	// Token store is always created since tokens work in both auth modes
	store, err := tokenstore.NewStore(backend, dataDir, log)
	if err != nil {
		return nil, log.WrapErr(err, "failed to create token store")
	}
	return store, nil
}

func buildAuthConfig(ctx context.Context, cfg Config, authType domain.AuthType, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) (auth.Config, error) {
	authConfig := auth.Config{
		Enabled:  cfg.Auth.Enabled,
		AuthType: authType,
		Username: cfg.Auth.Username,
	}

	// Token config is always required (tokens work in all auth modes)
	secret, expiry, err := loadTokenConfig(ctx, cfg, backend, dataDir, log)
	if err != nil {
		return auth.Config{}, err
	}
	authConfig.TokenSecret = secret
	authConfig.TokenExpiry = expiry

	accessTokenTTL := 15 * time.Minute // default
	if cfg.Auth.AccessTokenTTL != "" {
		parsed, err := time.ParseDuration(cfg.Auth.AccessTokenTTL)
		if err != nil {
			return auth.Config{}, fmt.Errorf("invalid auth.access_token_ttl %q: %w", cfg.Auth.AccessTokenTTL, err)
		}
		if parsed <= 0 {
			return auth.Config{}, fmt.Errorf("auth.access_token_ttl must be positive")
		}
		if parsed > auth.MaxAccessTokenLifetime {
			return auth.Config{}, fmt.Errorf("auth.access_token_ttl must not exceed %v", auth.MaxAccessTokenLifetime)
		}
		accessTokenTTL = parsed
	}
	authConfig.AccessTokenTTL = accessTokenTTL

	return authConfig, nil
}

func loadTokenConfig(ctx context.Context, cfg Config, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) ([]byte, time.Duration, error) {
	secret, err := loadTokenSecret(ctx, cfg, backend, dataDir, log)
	if err != nil {
		return nil, 0, err
	}

	expiry, err := parseTokenExpiry(cfg.Auth.TokenExpiry)
	if err != nil {
		return nil, 0, err
	}

	return secret, expiry, nil
}

// TokenSecretEnvVar is the environment variable for the JWT signing secret.
// SECURITY: This takes priority over config file to allow secure secret injection.
const TokenSecretEnvVar = "GORDON_AUTH_TOKEN_SECRET" //nolint:gosec // This is an env var name, not a credential

func loadTokenSecret(ctx context.Context, cfg Config, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) ([]byte, error) {
	// SECURITY: Priority order for token secret:
	// 1. Environment variable (most secure - no disk exposure)
	// 2. Secrets backend (pass/sops - encrypted)
	// 3. Config file path (least preferred)

	const minTokenSecretLength = 32

	// Check environment variable first
	if envSecret := os.Getenv(TokenSecretEnvVar); envSecret != "" {
		if len(envSecret) < minTokenSecretLength {
			return nil, fmt.Errorf("token secret from %s must be at least %d bytes (got %d)", TokenSecretEnvVar, minTokenSecretLength, len(envSecret))
		}
		log.Debug().Msg("using token secret from environment variable")
		return []byte(envSecret), nil
	}

	// Fall back to config-specified path via secrets backend
	if cfg.Auth.TokenSecret == "" {
		return nil, fmt.Errorf("token_secret is required for JWT token generation; set %s environment variable or configure auth.token_secret", TokenSecretEnvVar)
	}

	secret, err := loadSecret(ctx, backend, cfg.Auth.TokenSecret, dataDir, log)
	if err != nil {
		return nil, log.WrapErr(err, "failed to load token secret")
	}

	if len(secret) < minTokenSecretLength {
		return nil, fmt.Errorf("token_secret must be at least %d bytes (got %d); use a strong random secret", minTokenSecretLength, len(secret))
	}

	return []byte(secret), nil
}

func parseTokenExpiry(expiry string) (time.Duration, error) {
	if expiry == "" {
		return 0, nil
	}

	parsed, err := duration.Parse(expiry)
	if err != nil {
		return 0, fmt.Errorf("invalid token_expiry: %w", err)
	}

	return parsed, nil
}

// loadSecret loads a secret from the configured backend.
func loadSecret(ctx context.Context, backend domain.SecretsBackend, path, dataDir string, log zerowrap.Logger) (string, error) {
	switch backend {
	case domain.SecretsBackendPass:
		provider := secrets.NewPassProvider(log)
		return provider.GetSecret(ctx, path)
	case domain.SecretsBackendSops:
		provider := secrets.NewSopsProvider(log)
		return provider.GetSecret(ctx, path)
	case domain.SecretsBackendUnsafe:
		// For unsafe backend, path is relative to dataDir/secrets/.
		return readUnsafeSecret(dataDir, path)
	default:
		return "", fmt.Errorf("unknown secrets backend: %s", backend)
	}
}

func readFileBeneath(root, cleanedRelPath string) ([]byte, error) {
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open secrets root: %w", err)
	}
	defer unix.Close(rootFD)

	parts := strings.Split(filepath.ToSlash(cleanedRelPath), "/")
	dirFD := rootFD
	var closeDirFDs []int
	defer func() {
		for i := len(closeDirFDs) - 1; i >= 0; i-- {
			_ = unix.Close(closeDirFDs[i])
		}
	}()

	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid secret path: path must stay under dataDir/secrets")
		}
		last := i == len(parts)-1
		if last {
			fd, err := unix.Openat(dirFD, part, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return nil, fmt.Errorf("failed to read secret file: %w", err)
			}
			defer unix.Close(fd)
			var st unix.Stat_t
			if err := unix.Fstat(fd, &st); err != nil {
				return nil, fmt.Errorf("failed to stat secret file: %w", err)
			}
			if st.Mode&unix.S_IFMT != unix.S_IFREG {
				return nil, fmt.Errorf("invalid secret path: secret must be a regular file")
			}
			data, err := os.ReadFile(fmt.Sprintf("/proc/self/fd/%d", fd))
			if err != nil {
				return nil, fmt.Errorf("failed to read secret file: %w", err)
			}
			return data, nil
		}

		nextFD, err := unix.Openat(dirFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return nil, fmt.Errorf("failed to open secret path component: %w", err)
		}
		closeDirFDs = append(closeDirFDs, nextFD)
		dirFD = nextFD
	}

	return nil, fmt.Errorf("invalid secret path: empty path")
}

type unsafeSecretProvider struct {
	dataDir string
}

func (p unsafeSecretProvider) Name() string { return string(domain.SecretsBackendUnsafe) }

func (p unsafeSecretProvider) IsAvailable() bool { return true }

func (p unsafeSecretProvider) GetSecret(_ context.Context, path string) (string, error) {
	return readUnsafeSecret(p.dataDir, path)
}

func createStandaloneServiceSecretProvider(backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) out.SecretProvider {
	switch backend {
	case domain.SecretsBackendPass:
		return secrets.NewPassProvider(log)
	case domain.SecretsBackendSops:
		return secrets.NewSopsProvider(log)
	case domain.SecretsBackendUnsafe:
		return unsafeSecretProvider{dataDir: dataDir}
	default:
		return nil
	}
}

func readUnsafeSecret(dataDir, secretPath string) (string, error) {
	if filepath.IsAbs(secretPath) {
		return "", fmt.Errorf("invalid secret path: absolute paths are not allowed")
	}
	cleaned := filepath.Clean(secretPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid secret path: path must stay under dataDir/secrets")
	}

	root := filepath.Clean(filepath.Join(dataDir, "secrets"))
	data, err := readFileBeneath(root, cleaned)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// proxyConfigResult holds parsed proxy and blob chunk size config.
type proxyConfigResult struct {
	proxyConfig      proxy.Config
	maxBlobChunkSize int64
	maxBlobSize      int64
}

type configWatcher interface {
	Watch(ctx context.Context, onChange func()) error
}

// publicTLSReconciler is the interface for reconciling public TLS certificates.
type publicTLSReconciler interface {
	Reconcile(context.Context) error
}

type configReloader interface {
	Reload(ctx context.Context) error
}

type proxyConfigUpdater interface {
	UpdateConfig(config proxy.Config)
}

type reloadTrigger interface {
	Trigger(ctx context.Context) error
}

type loadedConfigApplier interface {
	ApplyLoadedConfig(ctx context.Context) error
}

type reloadCoordinator struct {
	mu       sync.Mutex
	lastRun  time.Time
	debounce time.Duration

	configSvc            configReloader
	v                    *viper.Viper
	proxySvc             proxyConfigUpdater
	applyContainerConfig func(context.Context, Config) error
	registryLimits       interface {
		UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
	}
	eventBus  out.EventPublisher
	publicTLS publicTLSReconciler
	log       zerowrap.Logger
}

func newReloadCoordinator(v *viper.Viper, configSvc configReloader, proxySvc proxyConfigUpdater, registryLimits interface {
	UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
}, eventBus out.EventPublisher, publicTLS publicTLSReconciler, log zerowrap.Logger) *reloadCoordinator {
	return &reloadCoordinator{
		debounce:       500 * time.Millisecond,
		configSvc:      configSvc,
		v:              v,
		proxySvc:       proxySvc,
		registryLimits: registryLimits,
		eventBus:       eventBus,
		publicTLS:      publicTLS,
		log:            log,
	}
}

func (c *reloadCoordinator) SetRegistryLimits(limits interface {
	UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registryLimits = limits
}

func (c *reloadCoordinator) SetContainerConfigApplier(apply func(context.Context, Config) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.applyContainerConfig = apply
}

func (c *reloadCoordinator) Trigger(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.reloadLocked(ctx, true)
}

func (c *reloadCoordinator) ApplyLoadedConfig(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.reloadLocked(ctx, false)
}

func (c *reloadCoordinator) reloadLocked(ctx context.Context, loadConfig bool) error {
	now := time.Now()
	if !c.lastRun.IsZero() && now.Sub(c.lastRun) < c.debounce {
		c.log.Debug().Dur("since_last_reload", now.Sub(c.lastRun)).Msg("skipping config reload trigger due to debounce")
		return nil
	}

	if loadConfig {
		if err := c.configSvc.Reload(ctx); err != nil {
			c.log.Error().Err(err).Msg("failed to reload config")
			return fmt.Errorf("failed to reload config: %w", err)
		}
	}

	if err := c.applyLoadedConfig(ctx, now); err != nil {
		return err
	}

	return nil
}

func (c *reloadCoordinator) applyLoadedConfig(ctx context.Context, now time.Time) error {
	var reloadCfg Config
	if err := c.v.Unmarshal(&reloadCfg); err != nil {
		c.log.Error().Err(err).Msg("failed to unmarshal config on reload")
		return fmt.Errorf("failed to unmarshal config on reload: %w", err)
	}
	if err := validateEntrypointMigration(c.v, reloadCfg); err != nil {
		return err
	}
	if err := validateRetiredAppConfig(c.v); err != nil {
		return err
	}

	reloadedProxy, err := buildProxyConfig(reloadCfg, c.log)
	if err != nil {
		c.log.Error().Err(err).Msg("failed to parse proxy config on reload")
		return fmt.Errorf("failed to parse proxy config on reload: %w", err)
	}

	if c.applyContainerConfig != nil {
		if err := c.applyContainerConfig(ctx, reloadCfg); err != nil {
			c.log.Error().Err(err).Msg("failed to apply container config on reload")
			return fmt.Errorf("failed to apply container config on reload: %w", err)
		}
	}
	c.proxySvc.UpdateConfig(reloadedProxy.proxyConfig)
	if c.registryLimits != nil {
		c.registryLimits.UpdateBlobLimits(reloadedProxy.maxBlobChunkSize, reloadedProxy.maxBlobSize)
	}

	// Reconcile public TLS before publishing reload events so certificate
	// authorization reflects the loaded config even if event delivery fails.
	// A transient ACME issue must not abort the rest of the reload.
	if c.publicTLS != nil {
		if err := c.publicTLS.Reconcile(ctx); err != nil {
			c.log.Warn().Err(err).Msg("failed to reconcile public TLS certificates after reload, continuing")
		}
	}

	if c.eventBus != nil {
		if err := c.eventBus.Publish(domain.EventConfigReload, nil); err != nil {
			c.log.Error().Err(err).Msg("failed to publish config reload event")
			return fmt.Errorf("failed to publish config reload event: %w", err)
		}
	}

	c.lastRun = now

	c.log.Debug().Msg("config hot reload complete")
	return nil
}

// buildProxyConfig parses size-related config fields and builds the proxy config.
func buildProxyConfig(cfg Config, log zerowrap.Logger) (*proxyConfigResult, error) {
	maxProxyBodySize := int64(512 << 20) // 512MB default
	if cfg.Server.MaxProxyBodySize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxProxyBodySize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_proxy_body_size configuration", map[string]any{"value": cfg.Server.MaxProxyBodySize})
		}
		maxProxyBodySize = parsedSize
	}

	maxBlobChunkSize := int64(registry.DefaultMaxBlobChunkSize)
	if cfg.Server.MaxBlobChunkSize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxBlobChunkSize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_blob_chunk_size configuration", map[string]any{"value": cfg.Server.MaxBlobChunkSize})
		}
		maxBlobChunkSize = parsedSize
	}

	maxBlobSize := int64(registry.DefaultMaxBlobSize)
	if cfg.Server.MaxBlobSize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxBlobSize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_blob_size configuration", map[string]any{"value": cfg.Server.MaxBlobSize})
		}
		maxBlobSize = parsedSize
	}

	maxProxyResponseSize := int64(1 << 30) // 1GB default
	if cfg.Server.MaxProxyResponseSize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxProxyResponseSize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_proxy_response_size configuration", map[string]any{"value": cfg.Server.MaxProxyResponseSize})
		}
		maxProxyResponseSize = parsedSize
	}

	maxConcurrentConns := cfg.Server.MaxConcurrentConns
	if maxConcurrentConns < 0 {
		maxConcurrentConns = 10000 // default when explicitly set to -1
	}
	// 0 means no limit (as documented in proxy.Config)

	registryDomain, _ := resolveRegistryDomains(cfg)

	return &proxyConfigResult{
		proxyConfig: proxy.Config{
			RegistryDomain:     registryDomain,
			RegistryPort:       cfg.Server.RegistryPort,
			MaxBodySize:        maxProxyBodySize,
			MaxResponseSize:    maxProxyResponseSize,
			MaxConcurrentConns: maxConcurrentConns,
		},
		maxBlobChunkSize: maxBlobChunkSize,
		maxBlobSize:      maxBlobSize,
	}, nil
}

// buildDNSConfig parses the raw dns config section into a publictls.DNSConfig.
func buildDNSConfig(cfg Config) (publictls.DNSConfig, error) {
	defaults := publictls.DefaultDNSConfig()

	resolvers := cfg.DNS.Resolvers
	if len(resolvers) == 0 {
		resolvers = defaults.Resolvers
	}

	propagationTimeout := defaults.PropagationTimeout
	if cfg.DNS.PropagationTimeout != "" {
		parsed, err := time.ParseDuration(cfg.DNS.PropagationTimeout)
		if err != nil {
			return publictls.DNSConfig{}, fmt.Errorf("invalid dns.propagation_timeout: %w", err)
		}
		propagationTimeout = parsed
	}

	pollingInterval := defaults.PollingInterval
	if cfg.DNS.PollingInterval != "" {
		parsed, err := time.ParseDuration(cfg.DNS.PollingInterval)
		if err != nil {
			return publictls.DNSConfig{}, fmt.Errorf("invalid dns.polling_interval: %w", err)
		}
		pollingInterval = parsed
	}

	dnsCfg := publictls.DNSConfig{
		Resolvers:          append([]string(nil), resolvers...),
		PropagationTimeout: propagationTimeout,
		PollingInterval:    pollingInterval,
	}
	if err := dnsCfg.Validate(); err != nil {
		return publictls.DNSConfig{}, err
	}
	return dnsCfg, nil
}

func buildContainerServiceConfig(_ context.Context, v *viper.Viper, _ Config, _ *services, _ zerowrap.Logger) (container.Config, error) {
	return container.Config{
		NetworkPrefix: v.GetString("network_isolation.network_prefix"),
	}, nil
}

// createContainerService creates the container service with configuration.
func createContainerService(ctx context.Context, v *viper.Viper, cfg Config, svc *services, log zerowrap.Logger) (*container.Service, error) {
	containerConfig, err := buildContainerServiceConfig(ctx, v, cfg, svc, log)
	if err != nil {
		return nil, err
	}
	return container.NewService(svc.runtime, svc.envLoader, svc.eventBus, svc.logWriter, containerConfig), nil
}

type databaseBackupSettingsConfig struct {
	Enabled    bool
	Schedule   string
	StorageDir string
	Retention  struct {
		Hourly  int
		Daily   int
		Weekly  int
		Monthly int
	}
}

func databaseBackupSettings(cfg Config) databaseBackupSettingsConfig {
	out := databaseBackupSettingsConfig{
		Enabled:    cfg.Backups.Databases.Enabled,
		Schedule:   cfg.Backups.Databases.Schedule,
		StorageDir: cfg.Backups.Databases.StorageDir,
	}
	out.Retention.Hourly = cfg.Backups.Databases.Retention.Hourly
	out.Retention.Daily = cfg.Backups.Databases.Retention.Daily
	out.Retention.Weekly = cfg.Backups.Databases.Retention.Weekly
	out.Retention.Monthly = cfg.Backups.Databases.Retention.Monthly
	// Legacy [backups] keys intentionally override new database defaults when
	// backups.enabled is true, preserving existing working pg_dump schedules.
	// Otherwise, prefer the already-populated backups.databases.* values.
	if cfg.Backups.Enabled {
		out.Enabled = true
		if cfg.Backups.Schedule != "" {
			out.Schedule = cfg.Backups.Schedule
		}
		if cfg.Backups.StorageDir != "" {
			out.StorageDir = cfg.Backups.StorageDir
		}
	} else {
		if out.Schedule == "" {
			out.Schedule = cfg.Backups.Schedule
		}
		if out.StorageDir == "" {
			out.StorageDir = cfg.Backups.StorageDir
		}
	}
	if out.Retention.Hourly == 0 {
		out.Retention.Hourly = cfg.Backups.Retention.Hourly
	}
	if out.Retention.Daily == 0 {
		out.Retention.Daily = cfg.Backups.Retention.Daily
	}
	if out.Retention.Weekly == 0 {
		out.Retention.Weekly = cfg.Backups.Retention.Weekly
	}
	if out.Retention.Monthly == 0 {
		out.Retention.Monthly = cfg.Backups.Retention.Monthly
	}
	return out
}

func createBackupService(cfg Config, svc *services, log zerowrap.Logger) (*filesystem.BackupStorage, *backup.Service, error) {
	dbCfg := databaseBackupSettings(cfg)
	if !dbCfg.Enabled {
		return nil, nil, nil
	}

	storageDir := dbCfg.StorageDir
	if storageDir == "" {
		dataDir := resolveDataDir(cfg.Server.DataDir)
		storageDir = filepath.Join(dataDir, "backups")
	}

	backupStorage, err := filesystem.NewBackupStorage(storageDir, log)
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to create backup storage")
	}

	retention, err := validateBackupRetention(cfg)
	if err != nil {
		return nil, nil, log.WrapErr(err, "invalid backup retention policy")
	}

	backupCfg := domain.BackupConfig{
		Enabled:    dbCfg.Enabled,
		StorageDir: storageDir,
		Retention:  retention,
	}

	backupSvc := backup.NewService(svc.runtime, backupStorage, backupCfg, log)

	log.Info().
		Str("storage_dir", storageDir).
		Msg("backup service initialized")

	return backupStorage, backupSvc, nil
}

func createVolumeBackupService(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger) (out.VolumeBackupStorage, *backup.VolumeService, domain.VolumeBackupConfig, error) {
	if !cfg.Backups.Volumes.Enabled {
		return nil, nil, domain.VolumeBackupConfig{}, nil
	}
	volumeCfg, err := validateVolumeBackupConfig(cfg)
	if err != nil {
		return nil, nil, domain.VolumeBackupConfig{}, log.WrapErr(err, "invalid volume backup configuration")
	}

	storage, err := s3storage.NewVolumeBackupStorage(ctx, volumeCfg)
	if err != nil {
		return nil, nil, domain.VolumeBackupConfig{}, log.WrapErr(err, "failed to create volume backup storage")
	}

	volumeSvc := backup.NewVolumeService(svc.runtime, storage, volumeCfg, log)
	log.Info().
		Str("bucket", volumeCfg.S3Bucket).
		Str("prefix", volumeCfg.S3Prefix).
		Msg("volume backup service initialized")

	return storage, volumeSvc, volumeCfg, nil
}

func validateBackupRetention(cfg Config) (domain.RetentionPolicy, error) {
	dbCfg := databaseBackupSettings(cfg)
	if dbCfg.Retention.Hourly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.databases.retention.hourly cannot be negative")
	}
	if dbCfg.Retention.Daily < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.databases.retention.daily cannot be negative")
	}
	if dbCfg.Retention.Weekly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.databases.retention.weekly cannot be negative")
	}
	if dbCfg.Retention.Monthly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.databases.retention.monthly cannot be negative")
	}

	return domain.RetentionPolicy{
		Hourly:  dbCfg.Retention.Hourly,
		Daily:   dbCfg.Retention.Daily,
		Weekly:  dbCfg.Retention.Weekly,
		Monthly: dbCfg.Retention.Monthly,
	}, nil
}

func validateVolumeBackupConfig(cfg Config) (domain.VolumeBackupConfig, error) {
	volumeCfg := cfg.Backups.Volumes
	interval, err := parsePositiveDurationDefault(volumeCfg.Interval, "24h", "backups.volumes.interval")
	if err != nil {
		return domain.VolumeBackupConfig{}, err
	}
	timeout, err := parsePositiveDurationDefault(volumeCfg.Timeout, "2h", "backups.volumes.timeout")
	if err != nil {
		return domain.VolumeBackupConfig{}, err
	}
	compression, err := parseVolumeBackupCompression(volumeCfg.Compression)
	if err != nil {
		return domain.VolumeBackupConfig{}, err
	}
	maxConcurrency := volumeCfg.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = 2
	}
	helperImage := strings.TrimSpace(volumeCfg.HelperImage)
	if helperImage == "" {
		helperImage = "alpine:3.20"
	}
	volumePrefix := strings.TrimSpace(cfg.Volumes.Prefix)
	if volumePrefix == "" {
		volumePrefix = "gordon"
	}
	if compression == domain.VolumeBackupCompressionZstd && helperImage == "alpine:3.20" {
		return domain.VolumeBackupConfig{}, fmt.Errorf("backups.volumes.compression zstd requires a helper_image that provides zstd")
	}
	if err := validateVolumeBackupS3Settings(volumeCfg.Enabled, volumeCfg.Retention.Keep, maxConcurrency, volumeCfg.S3.Bucket, volumeCfg.S3.Region); err != nil {
		return domain.VolumeBackupConfig{}, err
	}
	return domain.VolumeBackupConfig{
		Enabled:        volumeCfg.Enabled,
		Interval:       interval,
		Compression:    compression,
		Retention:      domain.VolumeBackupRetentionPolicy{Keep: volumeCfg.Retention.Keep},
		Timeout:        timeout,
		MaxConcurrency: maxConcurrency,
		HelperImage:    helperImage,
		VolumePrefix:   volumePrefix,
		S3Bucket:       strings.TrimSpace(volumeCfg.S3.Bucket),
		S3Region:       strings.TrimSpace(volumeCfg.S3.Region),
		S3Prefix:       strings.TrimSpace(volumeCfg.S3.Prefix),
		S3Endpoint:     strings.TrimSpace(volumeCfg.S3.Endpoint),
		S3PathStyle:    volumeCfg.S3.PathStyle,
		S3SSEAlgorithm: strings.TrimSpace(volumeCfg.S3.SSEAlgorithm),
		S3SSEKMSKeyID:  strings.TrimSpace(volumeCfg.S3.SSEKMSKeyID),
	}, nil
}

func parsePositiveDurationDefault(raw, defaultValue, field string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultValue
	}
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", field)
	}
	return d, nil
}

func parseVolumeBackupCompression(raw string) (domain.VolumeBackupCompression, error) {
	if strings.TrimSpace(raw) == "" {
		raw = string(domain.VolumeBackupCompressionGzip)
	}
	compression := domain.VolumeBackupCompression(strings.ToLower(strings.TrimSpace(raw)))
	switch compression {
	case domain.VolumeBackupCompressionGzip, domain.VolumeBackupCompressionZstd:
		return compression, nil
	default:
		return "", fmt.Errorf("backups.volumes.compression must be one of: gzip, zstd")
	}
}

func validateVolumeBackupS3Settings(enabled bool, keep, maxConcurrency int, bucket, region string) error {
	if keep < 0 {
		return fmt.Errorf("backups.volumes.retention.keep cannot be negative")
	}
	if enabled && keep == 0 {
		return fmt.Errorf("backups.volumes.retention.keep must be positive when volume backups are enabled")
	}
	if maxConcurrency < 1 {
		return fmt.Errorf("backups.volumes.max_concurrency must be at least 1")
	}
	if enabled && strings.TrimSpace(bucket) == "" {
		return fmt.Errorf("backups.volumes.s3.bucket is required when volume backups are enabled")
	}
	if enabled && strings.TrimSpace(region) == "" {
		return fmt.Errorf("backups.volumes.s3.region is required when volume backups are enabled")
	}
	return nil
}

// registerEventHandlers registers event handlers. Push-triggered
// route creation (auto-route, previews, image-pushed deploy) is
// retired: pushes transfer OCI content only and apps deploy
// explicitly via `gordon apps deploy`. The pre-v2.50 route-engine
// deploy arms (config-reload redeploy, manual SIGUSR2 deploy,
// secrets-changed redeploy) are removed with the declarative-apps
// cutover: reload never activates app state, and app secret changes
// take effect on explicit deploy.
func registerEventHandlers(ctx context.Context, svc *services) (func(), error) {
	// Proxy cache invalidation on config reload (clears stale targets for removed routes)
	configReloadProxyHandler := proxy.NewConfigReloadProxyHandler(ctx, svc.proxySvc)
	if err := svc.eventBus.Subscribe(configReloadProxyHandler); err != nil {
		return nil, fmt.Errorf("failed to subscribe config reload proxy handler: %w", err)
	}

	cleanup := func() {
	}

	return cleanup, nil
}

// setupConfigHotReload sets up config hot reload.
func setupConfigHotReload(ctx context.Context, configSvc configWatcher, coordinator loadedConfigApplier) error {
	if err := configSvc.Watch(ctx, func() {
		_ = coordinator.ApplyLoadedConfig(ctx)
	}); err != nil {
		return fmt.Errorf("failed to watch config: %w", err)
	}

	return nil
}

func loopbackOnly(next http.Handler, log zerowrap.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}

		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			log.Warn().
				Str("path", r.URL.Path).
				Str("remote_addr", r.RemoteAddr).
				Msg("blocked non-loopback access on internal admin route")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Forbidden"})
			return
		}

		next.ServeHTTP(w, r)
	})
}

// hostRedirectEligible reports whether a known host should be redirected
// from plaintext to HTTPS: app hosts declared tls=never stay plain HTTP.
func hostRedirectEligible(svc *services, host string) bool {
	if svc.appHostIndex == nil {
		return true
	}
	entry, ok := svc.appHostIndex.Lookup(host)
	return !ok || entry.TLSMode != domain.AppTLSNever
}

// hostRequiresTLS reports whether an app host declared tls=always, which
// must never be served over plaintext. An unwired index has no always hosts.
func hostRequiresTLS(svc *services, host string) bool {
	if svc.appHostIndex == nil {
		return false
	}
	entry, ok := svc.appHostIndex.Lookup(host)
	return ok && entry.TLSMode == domain.AppTLSAlways
}

// createHTTPHandlers creates HTTP handlers with middleware.
// Returns three handlers: registry, HTTP proxy (with CIDR + onboarding), and HTTPS proxy.
func createHTTPHandlers(svc *services, cfg Config, log zerowrap.Logger, accessWriter out.AccessLogWriter) (http.Handler, http.Handler, http.Handler) {
	// Parse trusted proxies once for all middleware chains.
	// This ensures consistent IP extraction across logging, rate limiting, and auth.
	trustedNets := httphelper.ParseTrustedProxies(cfg.API.RateLimit.TrustedProxies)

	// Registry handler
	registryHandler := registry.NewHandler(svc.registrySvc, log, svc.maxBlobChunkSize, svc.maxBlobSize)
	svc.registryHandler = registryHandler
	if svc.reloadCoordinator != nil {
		svc.reloadCoordinator.SetRegistryLimits(registryHandler)
	}
	registryWithMiddleware, cidrAllowlistMiddleware, rateLimitMiddleware := buildRegistryHandlerWithMiddleware(
		svc,
		cfg,
		trustedNets,
		registryHandler,
		log,
	)

	registryMux := http.NewServeMux()
	registerAuthRoutes(registryMux, svc, trustedNets, cidrAllowlistMiddleware, rateLimitMiddleware, cfg, log)
	registryMux.Handle("/v2/", wrapRegistryForLocalMode(registryWithMiddleware, cfg, log))
	registerAdminRoutes(registryMux, svc, cfg, trustedNets, log)

	// Proxy handler. Registry-domain forwarding is gated on auth: with
	// auth disabled the registry stays local-only and public ingress can
	// never reach it through this proxy.
	proxyHandler := proxyadapter.NewHandler(svc.proxySvc, trustedNets, log).
		WithRegistryForwarding(cfg.Auth.Enabled)

	// HTTP proxy handler chain: HTTPS redirect for non-proxy clients, then CIDR allowlist
	proxyAllowedNets, proxyCIDRMiddleware := buildProxyCIDRAllowlistMiddleware(cfg, trustedNets, log)

	httpProxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
		middleware.HTTPSRedirectWithEligibility(proxyAllowedNets, effectiveProxyHTTPPort(cfg), effectiveProxyTLSPort(cfg), cfg.Server.ForceHTTPSRedirect, log, func(host string) bool {
			return svc.proxySvc.IsKnownHost(context.Background(), host)
		}, func(host string) bool {
			return hostRedirectEligible(svc, host)
		}, func(host string) bool {
			return hostRequiresTLS(svc, host)
		}),
	}
	if proxyCIDRMiddleware != nil {
		httpProxyMiddlewares = append(httpProxyMiddlewares, proxyCIDRMiddleware)
	}

	httpProxyWithMiddleware := otelhttp.NewHandler(
		middleware.Chain(httpProxyMiddlewares...)(proxyHandler),
		"gordon.proxy",
	)

	// Build the onboarding handler once if internal CA is available and TLS is enabled.
	var obHandler *onboarding.Handler
	if svc.caAdapter != nil && effectiveProxyTLSPort(cfg) != 0 {
		mobileconfigBytes := pkiadapter.GenerateMobileconfig(
			svc.caAdapter.RootCertificateDER(),
			svc.caAdapter.RootCommonName(),
		)
		obHandler = onboarding.NewHandler(
			svc.caAdapter.RootCertificate(),
			mobileconfigBytes,
			svc.caAdapter.RootFingerprint(),
			effectiveProxyHTTPPort(cfg),
			effectiveProxyTLSPort(cfg),
		)
	}

	// HTTP proxyMux: trusted proxy traffic flows through the normal proxy chain.
	// Direct clients get an onboarding gate (when CA is available) placed BEFORE
	// HTTPSRedirect so force_https_redirect cannot bypass onboarding.
	// ACME HTTP-01 challenge handler is registered before the catch-all "/" so
	// it gets first chance regardless of source IP.
	proxyMux := http.NewServeMux()

	// Register ACME HTTP-01 challenge handler before all other routes so
	// Let's Encrypt validation always succeeds, even for onboarding clients.
	if svc.publicTLSSvc != nil {
		proxyMux.Handle(acmehttp.Prefix, acmehttp.NewHandler(svc.publicTLSSvc))
	}

	if proxyCIDRMiddleware != nil && proxyAllowedNets == nil {
		// Invalid proxy_allowed_ips: deny all traffic (fail-closed).
		proxyMux.Handle("/", proxyCIDRMiddleware(httpProxyWithMiddleware))
	} else if obHandler != nil {
		proxyMux.Handle("/", directHTTPOnboardingGate(obHandler, proxyAllowedNets, httpProxyWithMiddleware, log))
	} else {
		proxyMux.Handle("/", httpProxyWithMiddleware)
	}

	// HTTPS proxy handler chain: security headers + proxy + CA onboarding
	// Onboarding routes live on the TLS port so Tailnet / direct clients
	// can click through the initial cert warning, install the CA, and
	// then trust all subsequent connections.
	// The middleware chain wraps the entire mux so onboarding routes also
	// get PanicRecovery, RequestLogger, and SecurityHeaders.
	httpsProxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}

	httpsMux := http.NewServeMux()
	if obHandler != nil && cfg.Server.GordonDomain != "" {
		gordonDomain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cfg.Server.GordonDomain)), ".")
		onboardingMux := http.NewServeMux()
		registerOnboardingRoutes(onboardingMux, obHandler)
		// Register onboarding paths host-gated so normal traffic hits
		// proxyHandler directly through the catch-all / pattern.
		httpsMux.Handle("GET /.well-known/gordon/", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("GET /.well-known/gordon/ca", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("GET /.well-known/gordon/ca.crt", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("GET /.well-known/gordon/ca.mobileconfig", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("/", proxyHandler)
	} else {
		httpsMux.Handle("/", proxyHandler)
	}

	httpsHandler := otelhttp.NewHandler(middleware.Chain(httpsProxyMiddlewares...)(httpsMux), "gordon.proxy.tls")

	// Wrap top-level handlers with access logging outside all gates
	// (loopbackOnly, denyAllHandler, CIDR allowlist) so every request —
	// including rejected probes — produces exactly one access-log line.
	var registryOut, proxyOut, httpsOut http.Handler = registryMux, proxyMux, httpsHandler
	if accessWriter != nil {
		excludeHC := cfg.Logging.AccessLog.ExcludeHealthChecks
		registryOut = middleware.AccessLogger(accessWriter, excludeHC, log, trustedNets)(registryOut)
		proxyOut = middleware.AccessLogger(accessWriter, excludeHC, log, trustedNets)(proxyOut)
		httpsOut = middleware.AccessLogger(accessWriter, excludeHC, log, trustedNets)(httpsOut)
	}
	svc.httpsProxyHandler = httpsOut

	return registryOut, proxyOut, httpsOut
}

// registerOnboardingRoutes registers CA onboarding well-known HTTP routes on
// the given mux. Both direct-HTTP and Gordon-domain HTTPS onboarding use this.
func registerOnboardingRoutes(mux *http.ServeMux, ob *onboarding.Handler) {
	mux.HandleFunc("GET /.well-known/gordon/", ob.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/ca", ob.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/ca.crt", ob.ServeCACert)
	mux.HandleFunc("GET /.well-known/gordon/ca.mobileconfig", ob.ServeMobileconfig)
}

// directHTTPOnboardingGate returns an http.Handler that splits HTTP traffic
// by source IP. Trusted proxy IPs flow through to the normal proxy chain.
// Direct clients are served the CA onboarding flow on allowed paths and
// receive 403 on everything else. This gate runs BEFORE HTTPSRedirect so
// force_https_redirect cannot bypass onboarding for direct clients.
func directHTTPOnboardingGate(ob *onboarding.Handler, proxyNets []*net.IPNet, proxyChain http.Handler, log zerowrap.Logger) http.Handler {
	// Build a small mux for direct-client onboarding paths.
	onboardingMux := http.NewServeMux()
	registerOnboardingRoutes(onboardingMux, ob)

	// Reserve ACME challenge path for future use.
	onboardingMux.HandleFunc("/.well-known/acme-challenge/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})

	// Catch-all: reject any other direct HTTP request.
	// Uses a method-aware split: GET writes a body, HEAD gets an empty 403.
	onboardingMux.HandleFunc("/", directHTTPForbidden)

	onboardingWithMiddleware := middleware.Chain(
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log), // Intentionally omit trusted proxy nets so direct onboarding logs use RemoteAddr only.
		middleware.SecurityHeaders,
	)(onboardingMux)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteIP := httphelper.ExtractRemoteIP(r.RemoteAddr)
		if httphelper.IsTrustedOrLocal(remoteIP, proxyNets) {
			proxyChain.ServeHTTP(w, r)
			return
		}
		onboardingWithMiddleware.ServeHTTP(w, r)
	})
}

// canonicalHostsEqual compares two hosts after normalising both: stripping
// port, trimming spaces, lowercasing, and removing trailing dot.
func canonicalHostsEqual(host, expected string) bool {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	expected = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(expected)), ".")
	return host == expected
}

// gordonDomainOnboardingGate returns a handler that serves onboarding routes
// only when the request host matches gordonDomain. For mismatched hosts it
// delegates to proxyHandler. gordonDomain must already be canonicalised
// (trimmed, lowered, trailing dot removed).
func gordonDomainOnboardingGate(gordonDomain string, onboardingMux, proxyHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gordonDomain != "" && canonicalHostsEqual(r.Host, gordonDomain) {
			onboardingMux.ServeHTTP(w, r)
			return
		}
		proxyHandler.ServeHTTP(w, r)
	})
}

// directHTTPForbidden responds with 403 for non-onboarding HTTP paths.
// HEAD requests get an empty body per HTTP semantics.
func directHTTPForbidden(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte("Only certificate onboarding is available over HTTP.\n"))
	}
}

func buildRegistryHandlerWithMiddleware(
	svc *services,
	cfg Config,
	trustedNets []*net.IPNet,
	registryHandler http.Handler,
	log zerowrap.Logger,
) (http.Handler, func(http.Handler) http.Handler, func(http.Handler) http.Handler) {
	registryMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}

	cidrAllowlistMiddleware := buildRegistryCIDRAllowlistMiddleware(cfg, trustedNets, log)
	if cidrAllowlistMiddleware != nil {
		registryMiddlewares = append(registryMiddlewares, cidrAllowlistMiddleware)
	}

	rateLimitMiddleware := buildRegistryRateLimitMiddleware(cfg, log)
	registryMiddlewares = append(registryMiddlewares, rateLimitMiddleware)

	appendRegistryAuthMiddleware(&registryMiddlewares, svc, cfg, trustedNets, log)

	registryWithOtel := otelhttp.NewHandler(
		middleware.Chain(registryMiddlewares...)(registryHandler),
		"gordon.registry",
	)
	return registryWithOtel, cidrAllowlistMiddleware, rateLimitMiddleware
}

// parseCIDRAllowlist parses a list of IPs/CIDRs, logs warnings for invalid entries,
// and returns the parsed nets. label is used in log messages (e.g. "registry_allowed_ips").
func parseCIDRAllowlist(ips []string, label string, log zerowrap.Logger) ([]*net.IPNet, bool) {
	if len(ips) == 0 {
		return nil, false
	}

	allowedNets := httphelper.ParseTrustedProxies(ips)
	if len(allowedNets) != len(ips) {
		for _, entry := range ips {
			if nets := httphelper.ParseTrustedProxies([]string{entry}); len(nets) == 0 {
				log.Warn().Str("entry", entry).Msgf("ignoring invalid %s entry", label)
			}
		}
	}

	if len(allowedNets) == 0 {
		log.Error().
			Strs(label, ips).
			Msgf("%s is set but no valid entries were parsed; will deny all traffic (fail-closed)", label)
		return nil, true // allInvalid
	}

	return allowedNets, false
}

// denyAllHandler returns a middleware that rejects every request with 403 Forbidden.
func denyAllHandler(label string, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log.Warn().
				Str(zerowrap.FieldClientIP, middleware.GetClientIP(r, trustedNets)).
				Msgf("access denied due to invalid %s configuration", label)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Forbidden"})
		})
	}
}

func buildRegistryCIDRAllowlistMiddleware(cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	allowedNets, allInvalid := parseCIDRAllowlist(cfg.Server.RegistryAllowedIPs, "registry_allowed_ips", log)
	if allInvalid {
		return denyAllHandler("registry_allowed_ips", trustedNets, log)
	}
	if allowedNets == nil {
		return nil
	}
	return middleware.RegistryCIDRAllowlist(allowedNets, trustedNets, log)
}

func buildProxyCIDRAllowlistMiddleware(cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) ([]*net.IPNet, func(http.Handler) http.Handler) {
	allowedNets, allInvalid := parseCIDRAllowlist(cfg.Server.ProxyAllowedIPs, "proxy_allowed_ips", log)
	if allInvalid {
		return nil, denyAllHandler("proxy_allowed_ips", trustedNets, log)
	}
	if allowedNets == nil {
		return nil, nil
	}

	log.Info().
		Strs("proxy_allowed_ips", cfg.Server.ProxyAllowedIPs).
		Msg("proxy origin IP allowlist enabled")

	return allowedNets, middleware.ProxyCIDRAllowlist(allowedNets, log)
}

func buildRegistryRateLimitMiddleware(cfg Config, log zerowrap.Logger) func(http.Handler) http.Handler {
	if cfg.API.RateLimit.Enabled {
		globalLimiter := ratelimit.NewMemoryStore(cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.Burst, log)
		ipLimiter := ratelimit.NewMemoryStore(cfg.API.RateLimit.PerIPRPS, cfg.API.RateLimit.Burst, log)
		return registry.RateLimitMiddleware(
			globalLimiter,
			ipLimiter,
			cfg.API.RateLimit.TrustedProxies,
			log,
		)
	}

	return registry.RateLimitMiddleware(nil, nil, nil, log)
}

func appendRegistryAuthMiddleware(registryMiddlewares *[]func(http.Handler) http.Handler, svc *services, cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) {
	if svc.authSvc != nil {
		internalAuth := middleware.InternalRegistryAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		*registryMiddlewares = append(*registryMiddlewares, middleware.RegistryAuthV2(svc.authSvc, internalAuth, trustedNets, log))
		return
	}

	if cfg.Auth.Enabled {
		log.Error().Msg("authentication service unavailable; registry requests will be denied")
		*registryMiddlewares = append(*registryMiddlewares, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "authentication service unavailable"})
			})
		})
	}
}

func registerAuthRoutes(
	registryMux *http.ServeMux,
	svc *services,
	trustedNets []*net.IPNet,
	cidrAllowlistMiddleware func(http.Handler) http.Handler,
	rateLimitMiddleware func(http.Handler) http.Handler,
	cfg Config,
	log zerowrap.Logger,
) {
	if svc.authHandler == nil {
		return
	}

	// Auth endpoints always get rate limiting, even if global rate limiting is disabled.
	// This prevents brute-force attacks against password/token endpoints.
	authRateLimitMiddleware := rateLimitMiddleware
	if !cfg.API.RateLimit.Enabled {
		authGlobalLimiter := ratelimit.NewMemoryStore(50, 100, log)
		authIPLimiter := ratelimit.NewMemoryStore(5, 10, log)
		authRateLimitMiddleware = registry.RateLimitMiddleware(authGlobalLimiter, authIPLimiter, cfg.API.RateLimit.TrustedProxies, log)
	}

	// Auth endpoints are NOT protected by auth - they're where clients authenticate
	// but still need rate limiting to prevent brute force attacks.
	authMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}
	if cidrAllowlistMiddleware != nil {
		authMiddlewares = append(authMiddlewares, cidrAllowlistMiddleware)
	}
	authMiddlewares = append(authMiddlewares, authRateLimitMiddleware)
	authWithMiddleware := otelhttp.NewHandler(
		middleware.Chain(authMiddlewares...)(svc.authHandler),
		"gordon.auth",
	)
	registryMux.Handle("/auth/", authWithMiddleware)
}

func wrapRegistryForLocalMode(registryWithMiddleware http.Handler, cfg Config, log zerowrap.Logger) http.Handler {
	if !cfg.Auth.Enabled {
		return loopbackOnly(registryWithMiddleware, log)
	}
	return registryWithMiddleware
}

func registerAdminRoutes(registryMux *http.ServeMux, svc *services, cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) {
	if svc.adminHandler == nil {
		return
	}

	if !cfg.Auth.Enabled {
		log.Warn().Msg("auth disabled: admin API endpoints are not registered")
		return
	}

	adminMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
	}

	if svc.authSvc != nil {
		// Create rate limiters for admin API - uses same config as registry.
		var globalLimiter, ipLimiter out.RateLimiter
		if cfg.API.RateLimit.Enabled {
			globalLimiter = ratelimit.NewMemoryStore(cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.Burst, log)
			ipLimiter = ratelimit.NewMemoryStore(cfg.API.RateLimit.PerIPRPS, cfg.API.RateLimit.Burst, log)
		}
		adminMiddlewares = append(adminMiddlewares, admin.AuthMiddleware(svc.authSvc, globalLimiter, ipLimiter, trustedNets, log))
	} else {
		adminMiddlewares = append(adminMiddlewares, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "authentication service unavailable"})
			})
		})
	}

	adminWithMiddleware := otelhttp.NewHandler(
		middleware.Chain(adminMiddlewares...)(svc.adminHandler),
		"gordon.admin",
	)
	registryMux.Handle("/admin/", adminWithMiddleware)
}

// runServers starts the HTTP servers and waits for shutdown.
// Signal handling notes:
// - SIGINT/SIGTERM: Triggers graceful shutdown via signal.NotifyContext
// - SIGUSR1: Triggers config reload without restart
// (SIGUSR2 manual route deploy was removed with the declarative-apps
// cutover; app deploys are explicit via `gordon apps deploy`.)
// The deferred signal.Stop calls ensure signal handlers are properly
// cleaned up before program exit, preventing signal handler leaks.
func runServers(ctx context.Context, v *viper.Viper, cfg Config, svc *services, reload reloadTrigger, cleanupHandlers func(), log zerowrap.Logger) error {
	// Initialize access log writer. Kept here (not in Run) to keep Run's cyclomatic
	// complexity within the project limit of 15.
	accessWriterConcrete, err := initAccessLog(cfg, log)
	if err != nil {
		return err
	}
	if accessWriterConcrete != nil {
		defer accessWriterConcrete.Close()
	}
	// Convert to interface only when non-nil to avoid the Go nil-interface pitfall
	// where a typed nil pointer becomes a non-nil interface value.
	var accessWriter out.AccessLogWriter
	if accessWriterConcrete != nil {
		accessWriter = accessWriterConcrete
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Set up SIGUSR1 for reload.
	// Note: signal.Stop must be called (via defer) to release the channel
	// and prevent signal handler leaks when the function returns.
	reloadChan := make(chan os.Signal, 1)
	signal.Notify(reloadChan, syscall.SIGUSR1)
	defer signal.Stop(reloadChan)

	errChan := make(chan error, 4)

	registryHandler, httpProxyHandler, httpsProxyHandler := createHTTPHandlers(svc, cfg, log, accessWriter)
	svc.httpProxyHandler = httpProxyHandler
	svc.httpsProxyHandler = httpsProxyHandler

	registrySrv, registryReady, internalRegistrySrv, internalRegistryReady := startRegistryServers(cfg, registryHandler, errChan, log)

	// closeStarted shuts down any servers that were started before an error occurred,
	// preventing leaked listeners during partial startup failures.
	closeStarted := func(servers ...*http.Server) {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		for _, srv := range servers {
			if srv != nil {
				if err := srv.Shutdown(shutdownCtx); err != nil {
					log.Error().Err(err).Msg("failed to shut down server during startup cleanup")
				}
			}
		}
	}

	proxySrv, proxyReady, tlsSrv, _, err := startProxyServers(cfg, httpProxyHandler, httpsProxyHandler, svc.pkiSvc, svc.publicTLSSvc, svc.trafficManager, log)
	if err != nil {
		closeStarted(registrySrv, internalRegistrySrv)
		return err
	}

	// Owner-only local administration socket. Started before readiness is
	// announced so the CLI can reach the daemon as soon as it is usable,
	// and closed on every exit path. TCP /admin/* registration is unchanged.
	localAdmin, err := startLocalAdminServer(svc, errChan, log)
	if err != nil {
		closeStarted(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		shutdownTrafficManagerForStartupCleanup(svc.trafficManager, log)
		return err
	}

	svc.tlsHTTPEntryPoints = tlsMuxHTTPServerNames(cfg)
	svc.smartHTTPEntryPoints = smartTCPHTTPServerNames(cfg)

	// Wait for both registry listeners and the HTTP proxy before applying traffic.
	if err := waitForServerReady(internalRegistryReady, errChan); err != nil {
		closeStarted(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		return err
	}
	if err := waitForCoreProxyReadyAndApplyTraffic(ctx, cfg, svc, registryReady, proxyReady, errChan); err != nil {
		closeStarted(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		return err
	}

	logEvent := log.Info().
		Int("proxy_port", cfg.Server.Port).
		Int("registry_port", cfg.Server.RegistryPort)
	if cfg.Server.TLSPort != 0 {
		logEvent = logEvent.Int("tls_port", cfg.Server.TLSPort)
	}
	logEvent.Msg("Gordon is running")

	startPublicTLSRuntimeWithWarning(ctx, svc.publicTLSRuntime, log)

	schedulerCleanup, err := startOptionalSchedulers(ctx, cfg, svc, log, v)
	if err != nil {
		shutdownTrafficManagerForStartupCleanup(svc.trafficManager, log)
		closeStarted(registrySrv, internalRegistrySrv, proxySrv, tlsSrv)
		return err
	}
	if schedulerCleanup != nil {
		defer schedulerCleanup()
	}

	// Reconcile declarative apps intended to run: recovery first, then
	// start missing/stopped instances without duplicating running ones.
	// Explicitly stopped apps stay stopped across reboot. This is the sole
	// boot recovery path — the pre-v2.50 configured-route recovery was
	// removed with the declarative-apps cutover. The periodic recovery
	// monitor starts here and is cancelled and joined on shutdown.
	reconcileAppsAtBoot(ctx, svc, log)

	waitForShutdown(ctx, errChan, reloadChan, reload, svc.eventBus, log)
	cleanupHandlers() // Stop debounce timers before draining containers
	// Stop app administration before tearing down traffic/runtime dependencies,
	// so no new mutation can begin during shutdown.
	localAdmin.Close()
	registrySrvs := []*http.Server{registrySrv, internalRegistrySrv}
	gracefulShutdown(registrySrvs, proxySrv, tlsSrv, svc.containerSvc, svc.proxySvc, svc.pkiSvc, svc.publicTLSSvc, svc.trafficManager, svc.appMonitor, svc.appState, log)
	return nil
}

// reconcileAppsAtBoot reconciles declarative apps intended to run,
// rebuilds the proxy host index from reconciled ACTIVE state, then
// starts the single periodic recovery monitor. Failures only warn: the
// monitor still starts so a partially failed boot self-heals.
func reconcileAppsAtBoot(ctx context.Context, svc *services, log zerowrap.Logger) {
	if svc.appDeploySvc == nil {
		return
	}
	if err := svc.appDeploySvc.ReconcileBoot(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to reconcile apps at boot")
	}
	if err := rebuildAppHostIndex(ctx, svc); err != nil {
		log.Warn().Err(err).Msg("failed to rebuild app host index at boot")
	}
	// Exactly one monitor per process: a second boot reconciliation must
	// not orphan a running loop in the daemon context.
	if svc.appMonitor == nil {
		svc.appMonitor = newAppMonitor(svc.appDeploySvc, log)
	}
	svc.appMonitor.Start(ctx)
}

func startPublicTLSRuntimeWithWarning(ctx context.Context, svc publicTLSRuntime, log zerowrap.Logger) {
	if err := startPublicTLSRuntime(ctx, svc, log); err != nil {
		log.Warn().Err(err).Msg("initial public ACME reconcile failed, continuing with renewal loop")
	}
}

func waitForCoreProxyReadyAndApplyTraffic(ctx context.Context, cfg Config, svc *services, registryReady <-chan struct{}, proxyReady <-chan struct{}, errChan <-chan error) error {
	if err := waitForServerReady(registryReady, errChan); err != nil {
		return err
	}
	if err := waitForServerReady(proxyReady, errChan); err != nil {
		return err
	}
	if err := applyTrafficRuntimeConfig(ctx, svc.trafficManager, cfg, svc.configSvc, svc.appHostIndex); err != nil {
		return err
	}
	return nil
}

func shutdownTrafficManagerForStartupCleanup(manager *trafficadapter.Manager, log zerowrap.Logger) {
	if manager == nil {
		return
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("failed to shut down traffic manager during startup cleanup")
	}
}

func waitForServerReady(ready <-chan struct{}, errChan <-chan error) error {
	if ready == nil {
		return nil
	}
	select {
	case <-ready:
		return nil
	case err := <-errChan:
		return err
	}
}

func startOptionalSchedulers(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger, v *viper.Viper) (func(), error) {
	schedulers := make([]*cronSvc.Scheduler, 0, 3)

	backupScheduler, err := startBackupScheduler(ctx, cfg, svc, log)
	if err != nil {
		return nil, err
	}
	if backupScheduler != nil {
		schedulers = append(schedulers, backupScheduler)
	}

	volumeBackupScheduler, err := startVolumeBackupScheduler(ctx, cfg, svc, log)
	if err != nil {
		return nil, err
	}
	if volumeBackupScheduler != nil {
		schedulers = append(schedulers, volumeBackupScheduler)
	}

	imageScheduler, err := startImagePruneScheduler(ctx, cfg, svc, log, func() int {
		return v.GetInt("images.prune.keep_last")
	})
	if err != nil {
		return nil, err
	}
	if imageScheduler != nil {
		schedulers = append(schedulers, imageScheduler)
	}

	if len(schedulers) == 0 {
		return nil, nil
	}

	return func() {
		for i := len(schedulers) - 1; i >= 0; i-- {
			schedulers[i].Stop()
		}
	}, nil
}

func startBackupScheduler(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger) (*cronSvc.Scheduler, error) {
	dbCfg := databaseBackupSettings(cfg)
	if !dbCfg.Enabled || svc == nil || svc.backupSvc == nil {
		return nil, nil
	}

	preset, err := resolveBackupSchedule(dbCfg.Schedule)
	if err != nil {
		return nil, err
	}

	scheduler := cronSvc.NewScheduler(log)
	err = scheduler.Add(
		"backup-scheduler",
		"Backups",
		domain.CronSchedule{Preset: preset},
		func(jobCtx context.Context) error {
			if err := svc.backupSvc.RunForSchedule(jobCtx, preset); err != nil {
				return err
			}
			log.Info().
				Str("schedule", string(preset)).
				Msg("scheduled backup run complete")
			return nil
		},
	)
	if err != nil {
		return nil, log.WrapErr(err, "failed to register backup schedule")
	}

	scheduler.Start(ctx)
	log.Info().
		Str("schedule", string(preset)).
		Msg("backup scheduler enabled")

	return scheduler, nil
}

func resolveBackupSchedule(raw string) (domain.BackupSchedule, error) {
	return resolveSchedulePreset(raw, "backups.databases.schedule", domain.ScheduleDaily)
}

func startVolumeBackupScheduler(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger) (*cronSvc.Scheduler, error) {
	if !cfg.Backups.Volumes.Enabled || svc == nil || svc.volumeBackupSvc == nil {
		return nil, nil
	}

	volumeCfg, err := validateVolumeBackupConfig(cfg)
	if err != nil {
		return nil, err
	}

	scheduler := cronSvc.NewScheduler(log)
	err = scheduler.Add(
		"volume-backup-scheduler",
		"Volume Backups",
		domain.CronSchedule{Interval: volumeCfg.Interval},
		func(jobCtx context.Context) error {
			if err := svc.volumeBackupSvc.RunVolumeBackupsForSchedule(jobCtx, ""); err != nil {
				return err
			}
			log.Info().
				Dur("interval", volumeCfg.Interval).
				Msg("scheduled volume backup run complete")
			return nil
		},
	)
	if err != nil {
		return nil, log.WrapErr(err, "failed to register volume backup schedule")
	}

	scheduler.Start(ctx)
	log.Info().
		Dur("interval", volumeCfg.Interval).
		Msg("volume backup scheduler enabled")

	return scheduler, nil
}

func startImagePruneScheduler(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger, keepLastGetter func() int) (*cronSvc.Scheduler, error) {
	if !cfg.Images.Prune.Enabled || svc == nil || svc.imageSvc == nil {
		return nil, nil
	}
	// The scheduler runs the same use case and planner as manual
	// execution. App presence is normal: apps exist on every real
	// server, and prune decides per candidate.
	if keepLastGetter == nil {
		keepLastGetter = func() int { return cfg.Images.Prune.KeepLast }
	}
	if keepLastGetter() < 0 {
		return nil, fmt.Errorf("images.prune.keep_last must be >= 0")
	}

	preset, err := resolveImagePruneSchedule(cfg.Images.Prune.Schedule)
	if err != nil {
		return nil, err
	}

	scheduler := cronSvc.NewScheduler(log)
	err = scheduler.Add(
		"image-prune",
		"Image prune",
		domain.CronSchedule{Preset: preset},
		func(jobCtx context.Context) error {
			keepLast := keepLastGetter()
			if keepLast < 0 {
				log.Warn().
					Int("configured_keep_last", keepLast).
					Int("fallback_keep_last", domain.DefaultImagePruneKeepLast).
					Msg("invalid images.prune.keep_last; using default")
				keepLast = domain.DefaultImagePruneKeepLast
			}

			report, err := svc.imageSvc.Prune(jobCtx, domain.ImagePruneOptions{
				KeepLast:      keepLast,
				PruneDangling: true,
				PruneRegistry: true,
			})
			if err != nil {
				return err
			}

			log.Info().
				Int("keep_last", keepLast).
				Int("eligible", report.Plan.CountByVerdict(domain.PruneVerdictEligible)).
				Int("protected", report.Plan.CountByVerdict(domain.PruneVerdictProtected)).
				Int("unknown", report.Plan.CountByVerdict(domain.PruneVerdictUnknown)).
				Int("deleted", len(report.Plan.Deleted)).
				Int("failures", len(report.Plan.Failures)).
				Int("inventory_gaps", len(report.Plan.Gaps)).
				Int("runtime_deleted", report.Runtime.DeletedCount).
				Int("registry_tags_removed", report.Registry.TagsRemoved).
				Int("registry_blobs_removed", report.Registry.BlobsRemoved).
				Msg("scheduled image prune complete")
			return nil
		},
	)
	if err != nil {
		return nil, log.WrapErr(err, "failed to register image prune schedule")
	}

	scheduler.Start(ctx)
	log.Info().
		Str("schedule", string(preset)).
		Int("keep_last", keepLastGetter()).
		Msg("image prune scheduler enabled")

	return scheduler, nil
}

func resolveImagePruneSchedule(raw string) (domain.BackupSchedule, error) {
	return resolveSchedulePreset(raw, "images.prune.schedule", domain.ScheduleDaily)
}

func resolveSchedulePreset(raw, name string, defaultVal domain.BackupSchedule) (domain.BackupSchedule, error) {
	schedule := domain.BackupSchedule(strings.ToLower(strings.TrimSpace(raw)))
	if schedule == "" {
		schedule = defaultVal
	}

	switch schedule {
	case domain.ScheduleHourly, domain.ScheduleDaily, domain.ScheduleWeekly, domain.ScheduleMonthly:
		return schedule, nil
	default:
		return "", fmt.Errorf("%s must be one of: hourly, daily, weekly, monthly", name)
	}
}

// waitForShutdown blocks on the event loop, handling server errors and
// Unix signals (reload, deploy, shutdown) until the context is cancelled.
func waitForShutdown(ctx context.Context, errChan <-chan error, reloadChan <-chan os.Signal, reload reloadTrigger, eventBus out.EventBus, log zerowrap.Logger) {
	for {
		select {
		case err := <-errChan:
			log.Error().Err(err).Msg("server error")
			return
		case <-reloadChan:
			log.Info().Msg("reload signal received (SIGUSR1)")
			_ = reload.Trigger(ctx)
		case <-ctx.Done():
			log.Info().Msg("shutdown signal received")
			return
		}
	}
}

// gracefulShutdown stops HTTP servers with a 30s timeout, then shuts down
// the container service and cleans up runtime files.
func gracefulShutdown(registrySrvs []*http.Server, proxySrv, tlsSrv *http.Server, containerSvc *container.Service, proxySvc *proxy.Service, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, trafficManager *trafficadapter.Manager, monitor *appMonitor, appState out.AppState, log zerowrap.Logger) {
	log.Info().Msg("shutting down Gordon...")

	// Phase 0: stop periodic app recovery first and wait for any in-flight
	// bounded pass, so no reconcile can race runtime, traffic, or state
	// teardown (and no goroutine is left behind).
	monitor.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// Phase 1: Stop ingress frontends (TLS, then proxy) — no new traffic accepted
	for _, srv := range []*http.Server{tlsSrv, proxySrv} {
		if srv == nil {
			continue
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Str("addr", srv.Addr).Msg("server shutdown error")
		}
	}

	if trafficManager != nil {
		if err := trafficManager.Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Msg("traffic manager shutdown error")
		}
	}

	// Stop PKI maintenance goroutines
	if pkiSvc != nil {
		pkiSvc.Stop()
	}

	// Stop public ACME TLS renewal loop
	if publicTLS != nil {
		if err := publicTLS.Stop(shutdownCtx); err != nil {
			log.Warn().Err(err).Msg("public TLS stop error")
		}
	}

	// Phase 2: Drain in-flight registry push sessions before stopping the backend
	if proxySvc != nil {
		log.Info().Msg("draining in-flight registry requests...")
		if drained := proxySvc.DrainRegistryInFlight(25 * time.Second); !drained {
			log.Warn().Int64("in_flight", proxySvc.RegistryInFlight()).Msg("registry drain timed out; some in-flight pushes may be interrupted")
		}
	}

	// Phase 3: Stop the registry backends.
	for _, registrySrv := range registrySrvs {
		if registrySrv == nil {
			continue
		}
		if err := registrySrv.Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Str("addr", registrySrv.Addr).Msg("server shutdown error")
		}
	}

	if err := containerSvc.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("error during container shutdown")
	}
	closeAppState(appState, log)

	cleanupInternalCredentials()
	log.Info().Msg("Gordon stopped")
}

func closeAppState(state out.AppState, log zerowrap.Logger) {
	if state == nil {
		return
	}
	if err := state.Close(); err != nil {
		log.Warn().Err(err).Msg("app state close error")
	}
}

// startProxyServers sets up the HTTP proxy server and, when tls_port != 0,
// an HTTPS proxy server with on-demand TLS certificates from the internal CA.
// certificateSelector implements a multi-source TLS certificate lookup.
// Priority: static certs → public ACME TLS → local PKI (internal CA).
type certificateSelector struct {
	staticCerts []staticTLSCertificate
	publicTLS   in.PublicTLSService
	localPKI    *pkiusecase.Service
}

type staticTLSCertificate struct {
	cert tls.Certificate
	leaf *x509.Certificate
}

// GetCertificate selects a TLS certificate based on the ClientHello SNI.
//
// Priority:
//  1. Static certs — exact SNI match (leaf VerifyHostname)
//  2. Public ACME TLS — if the host requires ACME coverage
//  3. Local PKI (internal CA) — fallback for all other hosts
//  4. nil, nil — if no source can serve the host
func (s *certificateSelector) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	// 1. Static certs — exact match via leaf VerifyHostname.
	if cert := matchingPreparedStaticCert(s.staticCerts, hello.ServerName); cert != nil {
		return cert, nil
	}

	// 2. Public ACME TLS.
	if s.publicTLS != nil {
		cert, err := s.publicTLS.GetCertificateForHost(hello.ServerName)
		if err == nil && cert != nil {
			return cert, nil
		}
		// nil, nil means this host is not an ACME-required route. Errors mean
		// public ACME cannot currently serve this host. In both cases, fall
		// through to local PKI instead of aborting the TLS handshake.
	}

	// 3. Local PKI (internal CA).
	if s.localPKI != nil {
		return s.localPKI.GetCertificate(hello)
	}

	return nil, nil
}

func prepareStaticTLSCertificates(certs []tls.Certificate) []staticTLSCertificate {
	prepared := make([]staticTLSCertificate, 0, len(certs))
	for _, cert := range certs {
		if cert.Leaf == nil && len(cert.Certificate) > 0 {
			leaf, err := x509.ParseCertificate(cert.Certificate[0])
			if err == nil {
				cert.Leaf = leaf
			}
		}
		prepared = append(prepared, staticTLSCertificate{cert: cert, leaf: cert.Leaf})
	}
	return prepared
}

func matchingPreparedStaticCert(certs []staticTLSCertificate, serverName string) *tls.Certificate {
	if serverName == "" {
		if len(certs) == 0 {
			return nil
		}
		return &certs[0].cert
	}
	for i := range certs {
		if certs[i].leaf == nil {
			continue
		}
		if err := certs[i].leaf.VerifyHostname(serverName); err == nil {
			return &certs[i].cert
		}
	}
	return nil
}

// matchingStaticCert returns a pointer to the first static certificate whose
// leaf verifies the given serverName. Returns nil if no match is found.
func matchingStaticCert(certs []tls.Certificate, serverName string) *tls.Certificate {
	return matchingPreparedStaticCert(prepareStaticTLSCertificates(certs), serverName)
}

func startProxyServers(cfg Config, httpHandler, httpsHandler http.Handler, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, trafficManager *trafficadapter.Manager, log zerowrap.Logger) (*http.Server, <-chan struct{}, *http.Server, <-chan struct{}, error) {
	var httpSrv *http.Server
	var httpReady <-chan struct{}

	needsTLS := hasTLSCapableEntrypoint(cfg)
	if !hasSmartTCPEntrypoint(cfg) && !needsTLS {
		return httpSrv, httpReady, nil, nil, nil
	}
	cleanupHTTP := func(err error) (*http.Server, <-chan struct{}, *http.Server, <-chan struct{}, error) {
		return httpSrv, httpReady, nil, nil, err
	}

	var tlsConfig *tls.Config
	if needsTLS {
		var err error
		tlsConfig, err = proxyTLSConfig(cfg, pkiSvc, publicTLS, log)
		if err != nil {
			return cleanupHTTP(err)
		}
	}
	if trafficManager == nil {
		return cleanupHTTP(fmt.Errorf("traffic manager is required when traffic entrypoints are enabled"))
	}
	registerTLSMuxHTTPServers(trafficManager, cfg, httpsHandler, tlsConfig, nil)
	registerSmartTCPHTTPServers(trafficManager, cfg, httpHandler, httpsHandler, tlsConfig, nil)

	return httpSrv, httpReady, nil, nil, nil
}

func hasSmartTCPEntrypoint(cfg Config) bool {
	for _, entryPoint := range cfg.EntryPoints {
		if entryPoint.Protocol == domain.EntryPointProtocolSmartTCP {
			return true
		}
	}
	return false
}

func hasTLSCapableEntrypoint(cfg Config) bool {
	for _, entryPoint := range cfg.EntryPoints {
		switch entryPoint.Protocol {
		case domain.EntryPointProtocolSmartTCP, domain.EntryPointProtocolTLSMux:
			return true
		}
	}
	return false
}

func effectivePublicTLSPort(cfg Config) int {
	return effectiveEntrypointPort(cfg, tlsCapableEntryPoint)
}

func effectiveProxyTLSPort(cfg Config) int {
	return effectivePublicTLSPort(cfg)
}

func effectiveProxyHTTPPort(cfg Config) int {
	return effectiveEntrypointPort(cfg, func(protocol domain.EntryPointProtocol) bool {
		return protocol == domain.EntryPointProtocolSmartTCP
	})
}

func effectiveEntrypointPort(cfg Config, match func(domain.EntryPointProtocol) bool) int {
	if entryPoint, ok := cfg.EntryPoints[traffic.DefaultEdgeEntryPointName]; ok && match(entryPoint.Protocol) {
		if port := portFromAddress(entryPoint.Address); port > 0 {
			return port
		}
	}

	var candidatePort int
	candidates := 0
	for name, entryPoint := range cfg.EntryPoints {
		if name == traffic.DefaultEdgeEntryPointName || !match(entryPoint.Protocol) {
			continue
		}
		port := portFromAddress(entryPoint.Address)
		if port == 0 {
			continue
		}
		candidatePort = port
		candidates++
	}
	if candidates == 1 {
		return candidatePort
	}
	return 0
}

func tlsCapableEntryPoint(protocol domain.EntryPointProtocol) bool {
	switch protocol {
	case domain.EntryPointProtocolSmartTCP, domain.EntryPointProtocolTLSMux:
		return true
	default:
		return false
	}
}

func effectiveHTTP01Port(cfg Config) int {
	if hasSmartTCPHTTP01Entrypoint(cfg) {
		return 80
	}
	return 0
}

func validatePublicTLSReadiness(cfg Config) error {
	mode, err := domain.ParseACMEChallengeMode(cfg.TLS.ACME.Challenge)
	if err != nil {
		return err
	}
	switch mode {
	case domain.ACMEChallengeCloudflareDNS01, domain.ACMEChallengeAuto:
		return nil
	case domain.ACMEChallengeHTTP01:
		return validateHTTP01ChallengeReadiness(cfg)
	default:
		return fmt.Errorf("%w: %q", domain.ErrACMEChallengeInvalid, mode)
	}
}

func validateEffectivePublicTLSReadiness(cfg Config, effective publictls.EffectiveChallenge) error {
	if effective.Mode != domain.ACMEChallengeHTTP01 {
		return nil
	}
	return validateHTTP01ChallengeReadiness(cfg)
}

func validateHTTP01ChallengeReadiness(cfg Config) error {
	if hasBoundHTTP01ChallengeListener(cfg) {
		return nil
	}
	return fmt.Errorf("%w: http-01 requires an actually bound HTTP-01 challenge listener on external :80", domain.ErrACMEChallengeInvalid)
}

func hasBoundHTTP01ChallengeListener(cfg Config) bool {
	return hasSmartTCPHTTP01Entrypoint(cfg)
}

func hasSmartTCPHTTP01Entrypoint(cfg Config) bool {
	for _, entryPoint := range cfg.EntryPoints {
		if entryPoint.Protocol == domain.EntryPointProtocolSmartTCP && portFromAddress(entryPoint.Address) == 80 {
			return true
		}
	}
	return false
}

func portFromAddress(address string) int {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0
	}
	return port
}

func proxyTLSConfig(cfg Config, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, log zerowrap.Logger) (*tls.Config, error) {
	var staticCerts []tls.Certificate
	if cfg.Server.TLSCertFile != "" {
		staticCert, err := tls.LoadX509KeyPair(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("load TLS keypair: %w", err)
		}
		staticCerts = []tls.Certificate{staticCert}
		log.Info().
			Str("cert", cfg.Server.TLSCertFile).
			Str("key", cfg.Server.TLSKeyFile).
			Msg("loaded static TLS certificate (public ACME and internal CA handle remaining domains)")
	}
	selector := &certificateSelector{
		staticCerts: prepareStaticTLSCertificates(staticCerts),
		publicTLS:   publicTLS,
		localPKI:    pkiSvc,
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: selector.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}, nil
}

func registerSmartTCPHTTPServers(manager *trafficadapter.Manager, cfg Config, httpHandler, httpsHandler http.Handler, tlsConfig *tls.Config, previous map[string]struct{}) map[string]struct{} {
	if manager == nil {
		return previous
	}
	next := smartTCPHTTPServerNames(cfg)
	for name := range previous {
		if _, ok := next[name]; !ok {
			manager.SetSmartTCPHTTPServer(name, nil, nil)
			manager.SetSmartTCPTLSServer(name, nil, nil)
		}
	}
	var httpProtos http.Protocols
	httpProtos.SetHTTP1(true)
	httpProtos.SetUnencryptedHTTP2(true)
	for name := range next {
		if httpHandler != nil {
			manager.SetSmartTCPHTTPServer(name, httpHandler, &httpProtos)
		}
		if httpsHandler != nil && tlsConfig != nil {
			manager.SetSmartTCPTLSServer(name, httpsHandler, tlsConfig)
		}
	}
	return next
}

func smartTCPHTTPServerNames(cfg Config) map[string]struct{} {
	names := map[string]struct{}{}
	for name, entryPoint := range cfg.EntryPoints {
		if entryPoint.Protocol == domain.EntryPointProtocolSmartTCP {
			names[name] = struct{}{}
		}
	}
	return names
}

func registerTLSMuxHTTPServers(manager *trafficadapter.Manager, cfg Config, httpsHandler http.Handler, tlsConfig *tls.Config, previous map[string]struct{}) map[string]struct{} {
	if manager == nil {
		return previous
	}
	next := tlsMuxHTTPServerNames(cfg)
	for name := range previous {
		if _, ok := next[name]; !ok {
			manager.SetTLSHTTPServer(name, nil, nil)
		}
	}
	if httpsHandler == nil || tlsConfig == nil {
		return next
	}
	for name := range next {
		manager.SetTLSHTTPServer(name, httpsHandler, tlsConfig)
	}
	return next
}

func tlsMuxHTTPServerNames(cfg Config) map[string]struct{} {
	names := map[string]struct{}{}
	for name, entryPoint := range cfg.EntryPoints {
		domainEntryPoint := domain.EntryPoint{Name: name, Protocol: entryPoint.Protocol}
		if trafficManagerOwnsEntryPoint(domainEntryPoint) && domainEntryPoint.Protocol == domain.EntryPointProtocolTLSMux {
			names[name] = struct{}{}
		}
	}
	return names
}

func startRegistryServers(cfg Config, handler http.Handler, errChan chan<- error, log zerowrap.Logger) (*http.Server, <-chan struct{}, *http.Server, <-chan struct{}) {
	port := strconv.Itoa(cfg.Server.RegistryPort)
	externalAddr := net.JoinHostPort(cfg.Server.RegistryListenAddr, port)
	externalSrv, externalReady := startServer(externalAddr, handler, "registry", nil, errChan, log)
	internalReady := closedReadyChannel()
	if !needsInternalRegistryListener(cfg.Server.RegistryListenAddr) {
		return externalSrv, externalReady, nil, internalReady
	}
	internalAddr := net.JoinHostPort("127.0.0.1", port)
	internalSrv, internalReady := startServer(internalAddr, handler, "internal registry", nil, errChan, log)
	return externalSrv, externalReady, internalSrv, internalReady
}

func closedReadyChannel() <-chan struct{} {
	ready := make(chan struct{})
	close(ready)
	return ready
}

func needsInternalRegistryListener(listenAddr string) bool {
	addr := net.ParseIP(strings.TrimSpace(listenAddr))
	if addr == nil {
		return listenAddr != ""
	}
	return !addr.IsLoopback() && !addr.IsUnspecified()
}

// startServer starts an HTTP server, returning the server instance and a channel
// that closes once the listening socket is bound. This lets callers wait for the
// port to be ready before taking actions that depend on it (e.g. auto-start
// pulling from the local registry). The returned *http.Server can be used for
// graceful shutdown.
func startServer(addr string, handler http.Handler, name string, protocols *http.Protocols, errChan chan<- error, log zerowrap.Logger) (*http.Server, <-chan struct{}) {
	ready := make(chan struct{})

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		log.Info().Str("address", addr).Msgf("%s server starting", name)

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errChan <- fmt.Errorf("%s server error: %w", name, err)
			return
		}
		close(ready) // signal: port is bound and accepting connections

		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("%s server error: %w", name, err)
		}
	}()

	return server, ready
}

// SendReloadSignal sends SIGUSR1 to the running Gordon process.
func SendReloadSignal() error {
	process, _, err := findRunningProcess()
	if err != nil {
		return err
	}

	if err := process.Signal(syscall.SIGUSR1); err != nil {
		return fmt.Errorf("failed to send reload signal: %w", err)
	}

	return nil
}

// createPidFile creates a PID file for the Gordon process.
// SECURITY: Prefers secure locations (XDG_RUNTIME_DIR, ~/.gordon/run) over /tmp
// to prevent symlink attacks and unauthorized access.
func createPidFile(log zerowrap.Logger) string {
	pid := os.Getpid()

	// SECURITY: Prioritize secure locations over /tmp
	var locations []string

	// Try secure runtime directory first
	if runtimeDir, err := getSecureRuntimeDir(); err == nil {
		locations = append(locations, filepath.Join(runtimeDir, "gordon.pid"))
	}

	// Fall back to home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		locations = append(locations, filepath.Join(homeDir, ".gordon", "gordon.pid"))
	}

	// Last resort: /tmp (least secure due to world-writable)
	locations = append(locations, filepath.Join(os.TempDir(), "gordon.pid"))

	for _, location := range locations {
		// Ensure parent directory exists with secure permissions
		if err := os.MkdirAll(filepath.Dir(location), 0700); err != nil {
			continue
		}
		if err := os.WriteFile(location, fmt.Appendf(nil, "%d", pid), 0600); err == nil {
			log.Debug().Str("pid_file", location).Int("pid", pid).Msg("created PID file")
			return location
		}
	}

	log.Warn().Int("pid", pid).Msg("failed to create PID file in any location")
	return ""
}

// removePidFile removes the PID file.
func removePidFile(pidFile string, log zerowrap.Logger) {
	if err := os.Remove(pidFile); err != nil {
		log.Warn().Err(err).Str("pid_file", pidFile).Msg("failed to remove PID file")
	} else {
		log.Debug().Str("pid_file", pidFile).Msg("removed PID file")
	}
}

func pidFileLocations() []string {
	var locations []string
	seen := make(map[string]struct{})
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		locations = append(locations, path)
	}

	// Check secure runtime directory first
	if runtimeDir, err := getSecureRuntimeDir(); err == nil {
		add(filepath.Join(runtimeDir, "gordon.pid"))
	}

	// Also check canonical /run/user/<uid> runtime path. This handles cases where
	// Gordon started under systemd user services with runtime dir available, but
	// CLI invocations (e.g. non-interactive SSH) don't have XDG_RUNTIME_DIR set.
	runtimeByUID := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "gordon", "gordon.pid")
	add(runtimeByUID)

	// Check explicit XDG_RUNTIME_DIR if present in this process env.
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		add(filepath.Join(runtimeDir, "gordon.pid"))
	}

	// Check home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(homeDir, ".gordon", "gordon.pid"))
		// Legacy location for backward compatibility
		add(filepath.Join(homeDir, ".gordon.pid"))
	}

	// Legacy /tmp locations for backward compatibility
	add(filepath.Join(os.TempDir(), "gordon.pid"))
	add("/tmp/gordon.pid")

	return locations
}

// findRunningPidFile returns the first PID file whose PID belongs to a live process.
// Stale/invalid PID files are ignored and removed when possible.
func findRunningPidFile() (string, int, error) {
	return findRunningPidFileInLocations(pidFileLocations())
}

func findRunningPidFileInLocations(locations []string) (string, int, error) {
	foundAny := false

	for _, location := range locations {
		pidBytes, err := os.ReadFile(location)
		if err != nil {
			continue
		}

		foundAny = true

		var pid int
		if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil || pid <= 0 {
			_ = os.Remove(location)
			continue
		}

		if isProcessAlive(pid) {
			return location, pid, nil
		}

		_ = os.Remove(location)
	}

	if foundAny {
		return "", 0, fmt.Errorf("found stale gordon PID file(s), is Gordon running?")
	}

	return "", 0, fmt.Errorf("gordon PID file not found, is Gordon running?")
}

func findRunningProcess() (*os.Process, int, error) {
	_, pid, err := findRunningPidFile()
	if err != nil {
		return nil, 0, err
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to find process: %w", err)
	}

	return process, pid, nil
}

func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}

	return errors.Is(err, syscall.EPERM)
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
	v.SetDefault("deploy.pull_policy", "if-tag-changed")
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
