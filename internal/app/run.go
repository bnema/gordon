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

	// Boundaries
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"

	// Domain
	"github.com/bnema/gordon/internal/domain"

	// Packages
	"github.com/bnema/gordon/pkg/version"

	// Use cases
	"github.com/bnema/gordon/internal/usecase/auth"
	"github.com/bnema/gordon/internal/usecase/auto"
	"github.com/bnema/gordon/internal/usecase/auto/preview"
	"github.com/bnema/gordon/internal/usecase/backup"
	"github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	cronSvc "github.com/bnema/gordon/internal/usecase/cron"
	"github.com/bnema/gordon/internal/usecase/health"
	"github.com/bnema/gordon/internal/usecase/images"
	"github.com/bnema/gordon/internal/usecase/logs"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/gordon/internal/usecase/proxy"
	"github.com/bnema/gordon/internal/usecase/publictls"
	registrySvc "github.com/bnema/gordon/internal/usecase/registry"
	secretsSvc "github.com/bnema/gordon/internal/usecase/secrets"
	volumesSvc "github.com/bnema/gordon/internal/usecase/volumes"

	// Pkg
	"github.com/bnema/gordon/pkg/bytesize"
	"github.com/bnema/gordon/pkg/duration"
)

// Config holds the application configuration.
type Config struct {
	Server struct {
		Port                 int      `mapstructure:"port"`
		RegistryPort         int      `mapstructure:"registry_port"`
		GordonDomain         string   `mapstructure:"gordon_domain"`
		TLSPort              int      `mapstructure:"tls_port"`
		TLSCertFile          string   `mapstructure:"tls_cert_file"`
		TLSKeyFile           string   `mapstructure:"tls_key_file"`
		ForceHTTPSRedirect   bool     `mapstructure:"force_https_redirect"`
		DataDir              string   `mapstructure:"data_dir"`
		MaxProxyBodySize     string   `mapstructure:"max_proxy_body_size"`     // e.g., "512MB", "1GB"
		MaxBlobChunkSize     string   `mapstructure:"max_blob_chunk_size"`     // e.g., "512MB", "1GB"
		MaxBlobSize          string   `mapstructure:"max_blob_size"`           // e.g., "1GB", "2GB"
		MaxProxyResponseSize string   `mapstructure:"max_proxy_response_size"` // e.g., "1GB", "0" for no limit
		MaxConcurrentConns   int      `mapstructure:"max_concurrent_connections"`
		RegistryAllowedIPs   []string `mapstructure:"registry_allowed_ips"`
		ProxyAllowedIPs      []string `mapstructure:"proxy_allowed_ips"`
		RegistryListenAddr   string   `mapstructure:"registry_listen_address"`
	} `mapstructure:"server"`

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

	Backups struct {
		Enabled    bool   `mapstructure:"enabled"`
		Schedule   string `mapstructure:"schedule"`
		StorageDir string `mapstructure:"storage_dir"`
		Retention  struct {
			Hourly  int `mapstructure:"hourly"`
			Daily   int `mapstructure:"daily"`
			Weekly  int `mapstructure:"weekly"`
			Monthly int `mapstructure:"monthly"`
		} `mapstructure:"retention"`
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

	Telemetry telemetry.Config `mapstructure:"telemetry"`

	TLS struct {
		ACME struct {
			Enabled         bool   `mapstructure:"enabled"`
			Email           string `mapstructure:"email"`
			Challenge       string `mapstructure:"challenge"`
			ObtainBatchSize int    `mapstructure:"obtain_batch_size"`
		} `mapstructure:"acme"`
	} `mapstructure:"tls"`
}

// services holds all the services used by the application.
type services struct {
	runtime           *docker.Runtime
	eventBus          *eventbus.InMemory
	blobStorage       *filesystem.BlobStorage
	manifestStorage   *filesystem.ManifestStorage
	backupStorage     *filesystem.BackupStorage
	envLoader         out.EnvLoader
	logWriter         *logwriter.LogWriter
	tokenStore        out.TokenStore
	configSvc         *config.Service
	secretSvc         *secretsSvc.Service
	containerSvc      *container.Service
	backupSvc         *backup.Service
	registrySvc       *registrySvc.Service
	healthSvc         *health.Service
	logSvc            *logs.Service
	imageSvc          *images.Service
	volumeSvc         *volumesSvc.Service
	proxySvc          *proxy.Service
	authSvc           *auth.Service
	authHandler       *authhandler.Handler
	adminHandler      *admin.Handler
	internalRegUser   string
	internalRegPass   string
	previewStore      *filesystem.PreviewStore
	previewService    *preview.Service
	envDir            string
	maxBlobChunkSize  int64
	maxBlobSize       int64
	caAdapter         *pkiadapter.CA
	pkiSvc            *pkiusecase.Service
	reloadCoordinator *reloadCoordinator
	publicTLSSvc      in.PublicTLSService
	publicTLSRuntime  publicTLSRuntime
	registryHandler   interface {
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
	cleanupHandlers, err := registerEventHandlers(ctx, svc, cfg)
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

	return v, cfg, nil
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

	if err := si.initRuntimeAndProxy(); err != nil {
		return nil, err
	}

	si.svc.reloadCoordinator = newReloadCoordinator(v, si.svc.configSvc, si.svc.proxySvc, nil, si.svc.eventBus, si.svc.publicTLSSvc, log)

	si.initHandlers()

	return si.svc, nil
}

// initPKI initialises the internal CA and PKI service when TLS is enabled.
func (si *serviceInit) initPKI() error {
	if si.cfg.Server.TLSPort == 0 {
		si.log.Info().Msg("internal CA disabled (server.tls_port=0)")
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
	si.svc.pkiSvc = pkiusecase.NewService(si.ctx, caAdapter, si.svc.configSvc, si.log)
	return nil
}

// initPublicTLS initializes the public ACME TLS service if enabled.
func (si *serviceInit) initPublicTLS() error {
	if !si.cfg.TLS.ACME.Enabled {
		return nil
	}

	if si.cfg.Server.TLSPort == 0 {
		return fmt.Errorf("%w: tls.acme.enabled requires server.tls_port > 0", domain.ErrACMEChallengeInvalid)
	}

	ctx := si.ctx
	log := si.log

	publicTLSCfg := publictls.Config{
		Enabled:         si.cfg.TLS.ACME.Enabled,
		Email:           si.cfg.TLS.ACME.Email,
		Challenge:       si.cfg.TLS.ACME.Challenge,
		HTTPPort:        si.cfg.Server.Port,
		TLSPort:         si.cfg.Server.TLSPort,
		DataDir:         resolveDataDir(si.cfg.Server.DataDir),
		ObtainBatchSize: si.cfg.TLS.ACME.ObtainBatchSize,
	}

	tokenResolver := secrets.NewPublicTLSResolver(secrets.PublicTLSResolverConfig{})

	effective, err := publictls.ResolveEffectiveChallenge(ctx, publicTLSCfg, tokenResolver)
	if err != nil {
		return log.WrapErr(err, "resolve ACME challenge")
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
		Email:             si.cfg.TLS.ACME.Email,
		Challenge:         effective.Mode,
		Token:             effective.Token,
		Store:             store,
		HTTPChallengeSink: challenges,
	})
	if err != nil {
		return log.WrapErr(err, "create ACME issuer")
	}

	svc := publictls.NewService(publicTLSCfg, publictls.ServiceDeps{
		Routes:       si.svc.configSvc,
		Issuer:       issuer,
		Store:        store,
		ZoneResolver: zoneResolver,
		Challenges:   challenges,
		Effective:    effective,
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

	si.svc.secretSvc = secretsSvc.NewService(domainSecretStore, si.log, si.svc.eventBus)
	return nil
}

// initRuntimeAndProxy creates container, backup, registry, image, volume, and proxy services.
func (si *serviceInit) initRuntimeAndProxy() error {
	var err error

	if si.svc.containerSvc, err = createContainerService(si.ctx, si.v, si.cfg, si.svc, si.log); err != nil {
		return err
	}

	if si.svc.backupStorage, si.svc.backupSvc, err = createBackupService(si.cfg, si.svc, si.log); err != nil {
		return err
	}

	si.svc.registrySvc = registrySvc.NewService(si.svc.blobStorage, si.svc.manifestStorage, si.svc.eventBus)
	si.svc.imageSvc = images.NewService(si.svc.runtime, si.svc.manifestStorage, si.svc.blobStorage, si.log)
	si.svc.volumeSvc = volumesSvc.NewService(si.svc.runtime)

	injectTelemetryMetrics(si.cfg, si.svc, si.log)

	proxyCfg, err := buildProxyConfig(si.cfg, si.log)
	if err != nil {
		return err
	}
	si.svc.maxBlobChunkSize = proxyCfg.maxBlobChunkSize
	si.svc.maxBlobSize = proxyCfg.maxBlobSize
	si.svc.proxySvc = proxy.NewService(si.svc.runtime, si.svc.containerSvc, si.svc.configSvc, proxyCfg.proxyConfig)

	// Wire synchronous proxy cache invalidation for zero-downtime deployments.
	// The proxy service implements out.ProxyCacheInvalidator via InvalidateTarget().
	si.svc.containerSvc.SetProxyCacheInvalidator(si.svc.proxySvc)
	si.svc.containerSvc.SetProxyDrainWaiter(si.svc.proxySvc)
	return nil
}

// initHandlers creates the auth, health, log, preview, and admin handlers.
func (si *serviceInit) initHandlers() {
	if si.svc.authSvc != nil {
		internalAuth := authhandler.InternalAuth{
			Username: si.svc.internalRegUser,
			Password: si.svc.internalRegPass,
		}
		si.svc.authHandler = authhandler.NewHandler(si.svc.authSvc, internalAuth, si.log)
	}

	prober := httpprober.New()
	si.svc.healthSvc = health.NewService(si.svc.configSvc, si.svc.containerSvc, prober, si.log)

	si.svc.logSvc = logs.NewService(resolveLogFilePath(si.cfg), si.cfg.Logging.File.Enabled, si.svc.containerSvc, si.svc.runtime, si.log)

	initPreviewService(si.ctx, si.cfg, si.svc, si.log)

	si.svc.adminHandler = admin.NewHandler(admin.HandlerDeps{
		ConfigSvc:     si.svc.configSvc,
		AuthSvc:       si.svc.authSvc,
		ContainerSvc:  si.svc.containerSvc,
		HealthSvc:     si.svc.healthSvc,
		SecretSvc:     si.svc.secretSvc,
		LogSvc:        si.svc.logSvc,
		RegistrySvc:   si.svc.registrySvc,
		ReloadTrigger: si.svc.reloadCoordinator,
		Log:           si.log,
		BackupSvc:     si.svc.backupSvc,
		PreviewSvc:    si.svc.previewService,
		ImageSvc:      si.svc.imageSvc,
		VolumeSvc:     si.svc.volumeSvc,
		PublicTLSSvc:  si.svc.publicTLSSvc,
	})
}

// initPreviewService sets up the preview store, service, and TTL ticker.
func initPreviewService(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger) {
	previewStorePath := filepath.Join(resolveDataDir(cfg.Server.DataDir), "previews.json")
	svc.previewStore = filesystem.NewPreviewStore(previewStorePath)
	previewConfig := svc.configSvc.GetPreviewConfig()
	svc.previewService = preview.NewService(svc.previewStore, previewConfig.TTL).
		WithDeployer(svc.containerSvc).
		WithRouteManager(svc.configSvc).
		WithVolumeCloner(svc.runtime).
		WithRegistryDomain(svc.configSvc.GetRegistryDomain()).
		WithEnvLoader(svc.envLoader)
	if err := svc.previewService.Load(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to load previews")
	}

	// Derive sweep interval from TTL: half the TTL, capped at 1 hour, minimum 1 minute.
	sweepInterval := max(min(previewConfig.TTL/2, time.Hour), time.Minute)

	svc.previewService.StartTicker(ctx, sweepInterval, func(ctx context.Context, p domain.PreviewRoute) {
		teardownTrackedPreview(ctx, svc, p)
	}, func(ctx context.Context) {
		gcOrphanedPreviews(ctx, svc, previewConfig)
	})
}

// teardownTrackedPreview removes all resources for a tracked preview that has expired.
func teardownTrackedPreview(ctx context.Context, svc *services, p domain.PreviewRoute) {
	log := zerowrap.FromCtx(ctx)
	for _, containerName := range p.Containers {
		if err := svc.runtime.StopContainer(ctx, containerName); err != nil {
			log.Warn().Err(err).Str("container", containerName).Str("preview", p.Domain).Msg("failed to stop preview container")
		}
		if err := svc.runtime.RemoveContainer(ctx, containerName, true); err != nil {
			log.Warn().Err(err).Str("container", containerName).Str("preview", p.Domain).Msg("failed to remove preview container")
		}
	}
	for _, volName := range p.Volumes {
		if err := svc.runtime.RemoveVolume(ctx, volName, true); err != nil {
			log.Warn().Err(err).Str("volume", volName).Str("preview", p.Domain).Msg("failed to remove preview volume")
		}
	}
	// Remove network (naming convention: {networkPrefix}-{domain-sanitized}).
	networkPrefix := svc.configSvc.GetNetworkPrefix()
	networkName := networkPrefix + "-" + strings.ReplaceAll(p.Domain, ".", "-")
	if err := svc.runtime.RemoveNetwork(ctx, networkName); err != nil {
		log.Warn().Err(err).Str("network", networkName).Str("preview", p.Domain).Msg("failed to remove preview network")
	}
	// Remove route from config so proxy stops routing to this domain.
	if err := svc.configSvc.RemoveRoute(ctx, p.Domain); err != nil {
		if errors.Is(err, domain.ErrRouteNotFound) {
			log.Debug().Str("domain", p.Domain).Msg("preview route already removed from config")
		} else {
			log.Warn().Err(err).Str("domain", p.Domain).Msg("failed to remove preview route from config")
		}
	}
	svc.proxySvc.InvalidateTarget(ctx, p.Domain)
}

// gcOrphanedPreviews finds and tears down untracked preview containers.
func gcOrphanedPreviews(ctx context.Context, svc *services, previewConfig domain.PreviewConfig) {
	log := zerowrap.FromCtx(ctx)
	orphans := svc.previewService.CollectOrphans(ctx, svc.runtime, previewConfig.TagPatterns, previewConfig.Separator)
	for _, c := range orphans {
		orphanDomain := c.Labels[domain.LabelDomain]
		log.Warn().Str("container", c.Name).Str("image", c.Image).Str("domain", orphanDomain).
			Time("created", c.Created).Msg("orphaned preview container detected, cleaning up")

		// Order: stop → remove volumes/route → remove container → remove network.
		// Network removal must happen after container removal because the runtime
		// refuses to delete a network that still has containers attached.
		if err := svc.runtime.StopContainer(ctx, c.Name); err != nil {
			log.Warn().Err(err).Str("container", c.Name).Msg("failed to stop orphan container")
		}

		if orphanDomain != "" {
			if err := cleanupOrphanDomainResources(ctx, svc, orphanDomain); err != nil {
				log.Warn().Err(err).Str("container", c.Name).Str("domain", orphanDomain).
					Msg("orphan resource cleanup failed, deferring container removal to next scan")
				continue
			}
		}

		if err := svc.runtime.RemoveContainer(ctx, c.Name, true); err != nil {
			log.Warn().Err(err).Str("container", c.Name).Msg("failed to remove orphan container")
		}

		// Remove network after container is gone.
		if orphanDomain != "" {
			if err := removeOrphanNetwork(ctx, svc, strings.ReplaceAll(orphanDomain, ".", "-"), log); err != nil {
				log.Warn().Err(err).Str("container", c.Name).Msg("failed to remove orphan network after container removal")
			}
		}

		log.Info().Str("container", c.Name).Str("domain", orphanDomain).Msg("orphaned preview container cleaned up")
	}

	// Re-sync container state so stale routes disappear from routes list.
	if len(orphans) > 0 {
		if err := svc.containerSvc.SyncContainers(ctx); err != nil {
			log.Warn().Err(err).Msg("failed to sync containers after orphan GC")
		}
	}
}

// cleanupOrphanDomainResources removes volumes and route for an orphaned
// preview domain. Network removal is handled separately in gcOrphanedPreviews
// after the container is removed. Returns an error if any step fails so the
// caller can defer container removal until the next scan.
func cleanupOrphanDomainResources(ctx context.Context, svc *services, orphanDomain string) error {
	log := zerowrap.FromCtx(ctx)
	domainSanitized := strings.ReplaceAll(orphanDomain, ".", "-")
	var errs []error

	errs = append(errs, removeOrphanVolumes(ctx, svc, domainSanitized, log)...)
	errs = append(errs, removeOrphanRoute(ctx, svc, orphanDomain, log))
	svc.proxySvc.InvalidateTarget(ctx, orphanDomain)

	return errors.Join(errs...)
}

func removeOrphanVolumes(ctx context.Context, svc *services, domainSanitized string, log zerowrap.Logger) []error {
	_, volPrefix, _ := svc.configSvc.GetVolumeConfig()
	prefix := volPrefix + "-" + domainSanitized + "-"

	volumes, err := svc.runtime.ListVolumes(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("failed to list volumes for orphan cleanup")
		return []error{err}
	}

	var errs []error
	for _, v := range volumes {
		if !strings.HasPrefix(v.Name, prefix) {
			continue
		}
		if err := svc.runtime.RemoveVolume(ctx, v.Name, true); err != nil {
			log.Warn().Err(err).Str("volume", v.Name).Msg("failed to remove orphan volume")
			errs = append(errs, err)
		}
	}
	return errs
}

func removeOrphanNetwork(ctx context.Context, svc *services, domainSanitized string, log zerowrap.Logger) error {
	name := svc.configSvc.GetNetworkPrefix() + "-" + domainSanitized
	if err := svc.runtime.RemoveNetwork(ctx, name); err != nil {
		log.Warn().Err(err).Str("network", name).Msg("failed to remove orphan network")
		return err
	}
	return nil
}

func removeOrphanRoute(ctx context.Context, svc *services, orphanDomain string, log zerowrap.Logger) error {
	err := svc.configSvc.RemoveRoute(ctx, orphanDomain)
	if err == nil || errors.Is(err, domain.ErrRouteNotFound) {
		return nil
	}
	log.Warn().Err(err).Str("domain", orphanDomain).Msg("failed to remove orphan route")
	return err
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
	svc.containerSvc.SetMetrics(gordonMetrics)
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

func resolveServiceTokenExpiry(cfg Config) (time.Duration, error) {
	expiry, err := parseTokenExpiry(cfg.Auth.TokenExpiry)
	if err != nil {
		return 0, err
	}
	if expiry <= 0 {
		return serviceTokenDefaultTTL, nil
	}
	return expiry, nil
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

	configSvc      configReloader
	v              *viper.Viper
	proxySvc       proxyConfigUpdater
	registryLimits interface {
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

	reloadedProxy, err := buildProxyConfig(reloadCfg, c.log)
	if err != nil {
		c.log.Error().Err(err).Msg("failed to parse proxy config on reload")
		return fmt.Errorf("failed to parse proxy config on reload: %w", err)
	}

	c.proxySvc.UpdateConfig(reloadedProxy.proxyConfig)
	if c.registryLimits != nil {
		c.registryLimits.UpdateBlobLimits(reloadedProxy.maxBlobChunkSize, reloadedProxy.maxBlobSize)
	}

	if c.eventBus != nil {
		if err := c.eventBus.Publish(domain.EventConfigReload, nil); err != nil {
			c.log.Error().Err(err).Msg("failed to publish config reload event")
			return fmt.Errorf("failed to publish config reload event: %w", err)
		}
	}

	c.lastRun = now

	// Reconcile public TLS certificates after config reload.
	// Log and continue on failure — a transient ACME issue should not abort the entire reload.
	if c.publicTLS != nil {
		if err := c.publicTLS.Reconcile(ctx); err != nil {
			c.log.Warn().Err(err).Msg("failed to reconcile public TLS certificates after reload, continuing")
		}
	}

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

	return &proxyConfigResult{
		proxyConfig: proxy.Config{
			RegistryDomain:     cfg.Server.GordonDomain,
			RegistryPort:       cfg.Server.RegistryPort,
			MaxBodySize:        maxProxyBodySize,
			MaxResponseSize:    maxProxyResponseSize,
			MaxConcurrentConns: maxConcurrentConns,
		},
		maxBlobChunkSize: maxBlobChunkSize,
		maxBlobSize:      maxBlobSize,
	}, nil
}

// createContainerService creates the container service with configuration.
func createContainerService(ctx context.Context, v *viper.Viper, cfg Config, svc *services, log zerowrap.Logger) (*container.Service, error) {
	// Parse and validate container resource limits from config
	if cfg.Containers.CPULimit < 0 {
		return nil, fmt.Errorf("containers.cpu_limit must be >= 0 (got %f)", cfg.Containers.CPULimit)
	}
	if cfg.Containers.PidsLimit < 0 {
		return nil, fmt.Errorf("containers.pids_limit must be >= 0 (got %d)", cfg.Containers.PidsLimit)
	}
	var defaultMemoryLimit int64
	if cfg.Containers.MemoryLimit != "" {
		parsed, err := bytesize.Parse(cfg.Containers.MemoryLimit)
		if err != nil {
			return nil, fmt.Errorf("invalid containers.memory_limit %q: %w", cfg.Containers.MemoryLimit, err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("containers.memory_limit must be positive (got %q)", cfg.Containers.MemoryLimit)
		}
		defaultMemoryLimit = parsed
	}
	var defaultNanoCPUs int64
	if cfg.Containers.CPULimit > 0 {
		defaultNanoCPUs = int64(cfg.Containers.CPULimit * 1e9)
	}

	attachmentConfig := svc.configSvc.GetAttachmentConfig()

	containerConfig := container.Config{
		RegistryAuthEnabled:        cfg.Auth.Enabled,
		RegistryDomain:             cfg.Server.GordonDomain,
		RegistryPort:               cfg.Server.RegistryPort,
		InternalRegistryUsername:   svc.internalRegUser,
		InternalRegistryPassword:   svc.internalRegPass,
		PullPolicy:                 v.GetString("deploy.pull_policy"),
		VolumeAutoCreate:           v.GetBool("volumes.auto_create"),
		VolumePrefix:               v.GetString("volumes.prefix"),
		VolumePreserve:             v.GetBool("volumes.preserve"),
		NetworkIsolation:           v.GetBool("network_isolation.enabled"),
		NetworkPrefix:              v.GetString("network_isolation.network_prefix"),
		NetworkGroups:              attachmentConfig.NetworkGroups,
		NetworkInternal:            v.GetBool("network_isolation.internal"),
		Attachments:                attachmentConfig.Attachments,
		AllowedRegistries:          cfg.Images.AllowedRegistries,
		RequireImageDigest:         cfg.Images.RequireDigest,
		SecurityProfile:            cfg.Containers.SecurityProfile,
		ReadinessDelay:             v.GetDuration("deploy.readiness_delay"),
		ReadinessMode:              v.GetString("deploy.readiness_mode"),
		HealthTimeout:              v.GetDuration("deploy.health_timeout"),
		StabilizationDelay:         v.GetDuration("deploy.stabilization_delay"),
		TCPProbeTimeout:            v.GetDuration("deploy.tcp_probe_timeout"),
		HTTPProbeTimeout:           v.GetDuration("deploy.http_probe_timeout"),
		DrainDelay:                 v.GetDuration("deploy.drain_delay"),
		DrainMode:                  v.GetString("deploy.drain_mode"),
		DrainTimeout:               v.GetDuration("deploy.drain_timeout"),
		DefaultMemoryLimit:         defaultMemoryLimit,
		DefaultNanoCPUs:            defaultNanoCPUs,
		DefaultPidsLimit:           cfg.Containers.PidsLimit,
		AttachmentReadinessTimeout: v.GetDuration("deploy.attachment_readiness_timeout"),
	}
	if v.IsSet("deploy.drain_delay") {
		containerConfig.DrainDelayConfigured = true
		containerConfig.DrainDelay = v.GetDuration("deploy.drain_delay")
	}

	if containerConfig.RegistryAuthEnabled {
		if svc.authSvc == nil {
			return nil, fmt.Errorf("authentication service unavailable: cannot generate registry service token")
		}
		expiry, err := resolveServiceTokenExpiry(cfg)
		if err != nil {
			return nil, log.WrapErr(err, "failed to resolve service token expiry")
		}
		// Note: Service tokens are not auto-refreshed. If the token expires during
		// container runtime, the container will need to be recreated to get a new token.
		serviceToken, err := svc.authSvc.GenerateToken(ctx, serviceTokenSubject, []string{"pull"}, expiry)
		if err != nil {
			return nil, log.WrapErr(err, "failed to generate registry service token")
		}
		log.Info().
			Str("subject", serviceTokenSubject).
			Str("expiry", expiry.String()).
			Msg("generated service token for container registry access")
		containerConfig.ServiceTokenUsername = serviceTokenSubject
		containerConfig.ServiceToken = serviceToken
	} else {
		log.Warn().Msg("registry auth disabled; container image pulls will use unauthenticated mode")
	}

	return container.NewService(svc.runtime, svc.envLoader, svc.eventBus, svc.logWriter, containerConfig, svc.configSvc), nil
}

func createBackupService(cfg Config, svc *services, log zerowrap.Logger) (*filesystem.BackupStorage, *backup.Service, error) {
	if !cfg.Backups.Enabled {
		return nil, nil, nil
	}

	storageDir := cfg.Backups.StorageDir
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
		Enabled:    cfg.Backups.Enabled,
		StorageDir: storageDir,
		Retention:  retention,
	}

	backupSvc := backup.NewService(svc.runtime, backupStorage, svc.containerSvc, backupCfg, log)

	log.Info().
		Str("storage_dir", storageDir).
		Msg("backup service initialized")

	return backupStorage, backupSvc, nil
}

func validateBackupRetention(cfg Config) (domain.RetentionPolicy, error) {
	if cfg.Backups.Retention.Hourly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.retention.hourly cannot be negative")
	}
	if cfg.Backups.Retention.Daily < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.retention.daily cannot be negative")
	}
	if cfg.Backups.Retention.Weekly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.retention.weekly cannot be negative")
	}
	if cfg.Backups.Retention.Monthly < 0 {
		return domain.RetentionPolicy{}, fmt.Errorf("backups.retention.monthly cannot be negative")
	}

	return domain.RetentionPolicy{
		Hourly:  cfg.Backups.Retention.Hourly,
		Daily:   cfg.Backups.Retention.Daily,
		Weekly:  cfg.Backups.Retention.Weekly,
		Monthly: cfg.Backups.Retention.Monthly,
	}, nil
}

// registerEventHandlers registers all event handlers.
func registerEventHandlers(ctx context.Context, svc *services, cfg Config) (func(), error) {
	imagePushedHandler := container.NewImagePushedHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(imagePushedHandler); err != nil {
		return nil, fmt.Errorf("failed to subscribe image pushed handler: %w", err)
	}

	// Auto-route handler for creating routes from image labels
	autoRouteHandler := container.NewAutoRouteHandler(ctx, svc.configSvc, svc.containerSvc, svc.blobStorage, cfg.Server.GordonDomain).
		WithEnvExtractor(svc.runtime, svc.envDir)

	// Preview handler for creating preview environments from tagged images
	autoPreviewHandler := preview.NewAutoPreviewHandler(
		ctx,
		svc.configSvc,
		svc.previewService,
	)

	// Dispatcher routes image push events to either auto-route or preview handler
	dispatcher := auto.NewImagePushDispatcher(svc.configSvc, autoRouteHandler, autoPreviewHandler)
	if err := svc.eventBus.Subscribe(dispatcher); err != nil {
		return nil, fmt.Errorf("subscribe image push dispatcher: %w", err)
	}

	configReloadHandler := container.NewConfigReloadHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(configReloadHandler); err != nil {
		return nil, fmt.Errorf("failed to subscribe config reload handler: %w", err)
	}

	manualDeployHandler := container.NewManualDeployHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(manualDeployHandler); err != nil {
		return nil, fmt.Errorf("failed to subscribe manual deploy handler: %w", err)
	}

	secretsChangedHandler := container.NewSecretsChangedHandler(ctx, svc.containerSvc, svc.configSvc, container.DefaultSecretsDebounce)
	if err := svc.eventBus.Subscribe(secretsChangedHandler); err != nil {
		return nil, fmt.Errorf("failed to subscribe secrets changed handler: %w", err)
	}

	// Proxy cache invalidation on config reload (clears stale targets for removed routes)
	configReloadProxyHandler := proxy.NewConfigReloadProxyHandler(ctx, svc.proxySvc)
	if err := svc.eventBus.Subscribe(configReloadProxyHandler); err != nil {
		return nil, fmt.Errorf("failed to subscribe config reload proxy handler: %w", err)
	}

	cleanup := func() {
		secretsChangedHandler.Stop()
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

// syncAndAutoStart syncs existing containers and auto-starts if configured.
func syncAndAutoStart(ctx context.Context, svc *services, log zerowrap.Logger) {
	if err := svc.containerSvc.SyncContainers(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to sync existing containers")
	}

	if svc.configSvc.IsAutoRouteEnabled() {
		routes := svc.configSvc.GetRoutes(ctx)
		if err := svc.containerSvc.AutoStart(domain.WithInternalDeploy(ctx), routes); err != nil {
			log.Warn().Err(err).Msg("failed to auto-start containers")
		}
	}

	// Start background monitor to restart crashed containers.
	svc.containerSvc.StartMonitor(ctx)
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

	// Proxy handler
	proxyHandler := proxyadapter.NewHandler(svc.proxySvc, trustedNets, log)

	// HTTP proxy handler chain: HTTPS redirect for non-proxy clients, then CIDR allowlist
	proxyAllowedNets, proxyCIDRMiddleware := buildProxyCIDRAllowlistMiddleware(cfg, trustedNets, log)

	httpProxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
		middleware.HTTPSRedirect(proxyAllowedNets, cfg.Server.Port, cfg.Server.TLSPort, cfg.Server.ForceHTTPSRedirect, log, func(host string) bool {
			return svc.proxySvc.IsKnownHost(context.Background(), host)
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
	if svc.caAdapter != nil && cfg.Server.TLSPort != 0 {
		mobileconfigBytes := pkiadapter.GenerateMobileconfig(
			svc.caAdapter.RootCertificateDER(),
			svc.caAdapter.RootCommonName(),
		)
		obHandler = onboarding.NewHandler(
			svc.caAdapter.RootCertificate(),
			mobileconfigBytes,
			svc.caAdapter.RootFingerprint(),
			cfg.Server.Port,
			cfg.Server.TLSPort,
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
		httpsMux.Handle("GET /ca", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("GET /ca.crt", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
		httpsMux.Handle("GET /ca.mobileconfig", gordonDomainOnboardingGate(gordonDomain, onboardingMux, proxyHandler))
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

	return registryOut, proxyOut, httpsOut
}

// registerOnboardingRoutes registers all CA onboarding HTTP routes on the
// given mux. Both direct-HTTP and Gordon-domain HTTPS onboarding use this.
func registerOnboardingRoutes(mux *http.ServeMux, ob *onboarding.Handler) {
	mux.HandleFunc("GET /{$}", ob.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/", ob.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/ca", ob.ServeOnboardingPage)
	mux.HandleFunc("GET /.well-known/gordon/ca.crt", ob.ServeCACert)
	mux.HandleFunc("GET /.well-known/gordon/ca.mobileconfig", ob.ServeMobileconfig)
	mux.HandleFunc("GET /ca", ob.ServeOnboardingPage)
	mux.HandleFunc("GET /ca.crt", ob.ServeCACert)
	mux.HandleFunc("GET /ca.mobileconfig", ob.ServeMobileconfig)
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
	registryMux.Handle("/admin/", loopbackOnly(adminWithMiddleware, log))
}

// runServers starts the HTTP servers and waits for shutdown.
// Signal handling notes:
// - SIGINT/SIGTERM: Triggers graceful shutdown via signal.NotifyContext
// - SIGUSR1: Triggers config reload without restart
// - SIGUSR2: Triggers manual deploy for a specific route
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

	// Set up SIGUSR2 for manual deploy.
	deployChan := make(chan os.Signal, 1)
	signal.Notify(deployChan, syscall.SIGUSR2)
	defer signal.Stop(deployChan)

	errChan := make(chan error, 3)

	registryHandler, httpProxyHandler, httpsProxyHandler := createHTTPHandlers(svc, cfg, log, accessWriter)

	registryAddr := net.JoinHostPort(cfg.Server.RegistryListenAddr, strconv.Itoa(cfg.Server.RegistryPort))
	registrySrv, registryReady := startServer(registryAddr, registryHandler, "registry", nil, errChan, log)

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

	proxySrv, proxyReady, tlsSrv, tlsReady, err := startProxyServers(cfg, httpProxyHandler, httpsProxyHandler, svc.pkiSvc, svc.publicTLSSvc, errChan, log)
	if err != nil {
		closeStarted(registrySrv)
		return err
	}

	// Wait for all servers to bind their ports before auto-starting containers.
	// This prevents the race where auto-start pulls from the registry before it's listening.
	if err := waitForServerReady(registryReady, errChan); err != nil {
		closeStarted(registrySrv, proxySrv, tlsSrv)
		return err
	}
	if err := waitForServerReady(proxyReady, errChan); err != nil {
		closeStarted(registrySrv, proxySrv, tlsSrv)
		return err
	}
	if tlsReady != nil {
		if err := waitForServerReady(tlsReady, errChan); err != nil {
			closeStarted(registrySrv, proxySrv, tlsSrv)
			return err
		}
	}

	logEvent := log.Info().
		Int("proxy_port", cfg.Server.Port).
		Int("registry_port", cfg.Server.RegistryPort)
	if tlsSrv != nil {
		logEvent = logEvent.Int("tls_port", cfg.Server.TLSPort)
	}
	logEvent.Msg("Gordon is running")

	startPublicTLSRuntimeWithWarning(ctx, svc.publicTLSRuntime, log)

	schedulerCleanup, err := startOptionalSchedulers(ctx, cfg, svc, log, v)
	if err != nil {
		return err
	}
	if schedulerCleanup != nil {
		defer schedulerCleanup()
	}

	// Auto-start after servers are listening (registry port is now bound).
	syncAndAutoStart(ctx, svc, log)

	waitForShutdown(ctx, errChan, reloadChan, deployChan, reload, svc.eventBus, log)
	cleanupHandlers() // Stop debounce timers before draining containers
	gracefulShutdown(registrySrv, proxySrv, tlsSrv, svc.containerSvc, svc.proxySvc, svc.pkiSvc, svc.publicTLSSvc, log)
	return nil
}

func startPublicTLSRuntimeWithWarning(ctx context.Context, svc publicTLSRuntime, log zerowrap.Logger) {
	if err := startPublicTLSRuntime(ctx, svc, log); err != nil {
		log.Warn().Err(err).Msg("initial public ACME reconcile failed, continuing with renewal loop")
	}
}

func waitForServerReady(ready <-chan struct{}, errChan <-chan error) error {
	select {
	case <-ready:
		return nil
	case err := <-errChan:
		return err
	}
}

func startOptionalSchedulers(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger, v *viper.Viper) (func(), error) {
	schedulers := make([]*cronSvc.Scheduler, 0, 2)

	backupScheduler, err := startBackupScheduler(ctx, cfg, svc, log)
	if err != nil {
		return nil, err
	}
	if backupScheduler != nil {
		schedulers = append(schedulers, backupScheduler)
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
	if !cfg.Backups.Enabled || svc == nil || svc.backupSvc == nil {
		return nil, nil
	}

	preset, err := resolveBackupSchedule(cfg.Backups.Schedule)
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
	return resolveSchedulePreset(raw, "backups.schedule", domain.ScheduleDaily)
}

func startImagePruneScheduler(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger, keepLastGetter func() int) (*cronSvc.Scheduler, error) {
	if !cfg.Images.Prune.Enabled || svc == nil || svc.imageSvc == nil {
		return nil, nil
	}
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
				Int("runtime_deleted", report.Runtime.DeletedCount).
				Int64("runtime_reclaimed_bytes", report.Runtime.SpaceReclaimed).
				Int("registry_tags_removed", report.Registry.TagsRemoved).
				Int("registry_blobs_removed", report.Registry.BlobsRemoved).
				Int("registry_uploads_removed", report.Registry.UploadsRemoved).
				Int64("registry_upload_bytes_reclaimed", report.Registry.UploadSpaceReclaimed).
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
func waitForShutdown(ctx context.Context, errChan <-chan error, reloadChan, deployChan <-chan os.Signal, reload reloadTrigger, eventBus out.EventBus, log zerowrap.Logger) {
	for {
		select {
		case err := <-errChan:
			log.Error().Err(err).Msg("server error")
			return
		case <-reloadChan:
			log.Info().Msg("reload signal received (SIGUSR1)")
			_ = reload.Trigger(ctx)
		case <-deployChan:
			log.Info().Msg("deploy signal received (SIGUSR2)")
			domainName, err := readDeployRequest()
			if err != nil {
				log.Error().Err(err).Msg("failed to read deploy request")
				continue
			}
			payload := &domain.ManualDeployPayload{Domain: domainName}
			if err := eventBus.Publish(domain.EventManualDeploy, payload); err != nil {
				log.Error().Err(err).Str("domain", domainName).Msg("failed to publish manual deploy event")
			}
		case <-ctx.Done():
			log.Info().Msg("shutdown signal received")
			return
		}
	}
}

// gracefulShutdown stops HTTP servers with a 30s timeout, then shuts down
// the container service and cleans up runtime files.
func gracefulShutdown(registrySrv, proxySrv, tlsSrv *http.Server, containerSvc *container.Service, proxySvc *proxy.Service, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, log zerowrap.Logger) {
	log.Info().Msg("shutting down Gordon...")

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

	// Phase 3: Stop the registry backend
	if registrySrv != nil {
		if err := registrySrv.Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Str("addr", registrySrv.Addr).Msg("server shutdown error")
		}
	}

	containerSvc.StopMonitor()

	if err := containerSvc.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("error during container shutdown")
	}

	cleanupInternalCredentials()
	log.Info().Msg("Gordon stopped")
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

func startProxyServers(cfg Config, httpHandler, httpsHandler http.Handler, pkiSvc *pkiusecase.Service, publicTLS in.PublicTLSService, errChan chan<- error, log zerowrap.Logger) (*http.Server, <-chan struct{}, *http.Server, <-chan struct{}, error) {
	// HTTP listener (Cloudflare proxy + onboarding for direct clients)
	var httpProtos http.Protocols
	httpProtos.SetHTTP1(true)
	httpProtos.SetUnencryptedHTTP2(true)
	httpSrv, httpReady := startServer(
		fmt.Sprintf(":%d", cfg.Server.Port),
		httpHandler,
		"proxy-http",
		&httpProtos,
		errChan,
		log,
	)

	if cfg.Server.TLSPort == 0 {
		return httpSrv, httpReady, nil, nil, nil
	}

	// Load static cert into a local slice (not tls.Config.Certificates) so the
	// certificate selector has full control over priority ordering.
	var staticCerts []tls.Certificate
	if cfg.Server.TLSCertFile != "" {
		staticCert, err := tls.LoadX509KeyPair(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("load TLS keypair: %w", err)
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

	// HTTPS listener — static cert (if provided) takes priority by SNI match via
	// certificateSelector, then public ACME TLS, then on-demand internal CA certs.
	tlsConfig := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: selector.GetCertificate,
		NextProtos:     []string{"h2", "http/1.1"},
	}
	tlsSrv, tlsReady := startTLSServerWithConfig(
		fmt.Sprintf(":%d", cfg.Server.TLSPort),
		httpsHandler,
		"proxy-tls",
		tlsConfig,
		errChan,
		log,
	)

	return httpSrv, httpReady, tlsSrv, tlsReady, nil
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

// startTLSServerWithConfig starts an HTTPS server using a pre-built tls.Config
// (e.g. with GetCertificate for on-demand issuance via the internal CA).
// Returns the server instance and a channel that closes once the port is bound.
func startTLSServerWithConfig(addr string, handler http.Handler, name string, tlsCfg *tls.Config, errChan chan<- error, log zerowrap.Logger) (*http.Server, <-chan struct{}) {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ready := make(chan struct{})
	go func() {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errChan <- fmt.Errorf("%s listen: %w", name, err)
			return
		}
		tlsLn := tls.NewListener(ln, tlsCfg)
		log.Info().Str("addr", addr).Str("name", name).Msg("TLS server starting")
		close(ready)
		if err := srv.Serve(tlsLn); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("%s: %w", name, err)
		}
	}()

	return srv, ready
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

// getDeployRequestFile returns the path to the deploy request file.
func getDeployRequestFile() string {
	return filepath.Join(os.TempDir(), "gordon-deploy-request")
}

// writeDeployRequestFile creates the deploy request file exclusively with retry.
// This prevents race conditions when multiple deploy commands run simultaneously.
func writeDeployRequestFile(path string, data []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, writeErr := f.Write(data)
			f.Close()
			if writeErr != nil {
				_ = os.Remove(path)
				return writeErr
			}
			return nil
		}

		if os.IsExist(err) {
			if time.Now().After(deadline) {
				return fmt.Errorf("deploy request file still present after timeout; another deploy may be in progress")
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		return err
	}
}

// SendDeploySignal triggers a manual deploy for a specific route via SIGUSR2.
// Returns the domain name on success for the caller to display.
func SendDeploySignal(domain string) (string, error) {
	deployFile := getDeployRequestFile()
	if err := writeDeployRequestFile(deployFile, []byte(domain), 5*time.Second); err != nil {
		return "", fmt.Errorf("failed to write deploy request: %w", err)
	}

	// Find PID and send SIGUSR2
	process, _, err := findRunningProcess()
	if err != nil {
		_ = os.Remove(deployFile)
		return "", err
	}

	if err := process.Signal(syscall.SIGUSR2); err != nil {
		_ = os.Remove(deployFile)
		return "", fmt.Errorf("failed to send deploy signal: %w", err)
	}

	return domain, nil
}

// readDeployRequest reads and removes the deploy request file atomically.
// Returns empty string if file doesn't exist (may have been consumed by another handler).
func readDeployRequest() (string, error) {
	deployFile := getDeployRequestFile()

	// Rename to a temp file first to make the read-and-delete atomic
	tmpFile := deployFile + ".processing"
	if err := os.Rename(deployFile, tmpFile); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("deploy request file not found (may have been processed already)")
		}
		return "", fmt.Errorf("failed to acquire deploy request: %w", err)
	}

	data, err := os.ReadFile(tmpFile)
	_ = os.Remove(tmpFile) // Always clean up
	if err != nil {
		return "", fmt.Errorf("failed to read deploy request: %w", err)
	}

	return string(data), nil
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
	v.SetDefault("server.port", 8088)
	v.SetDefault("server.registry_port", 5000)
	v.SetDefault("server.tls_port", 8443)
	v.SetDefault("server.tls_cert_file", "")
	v.SetDefault("server.tls_key_file", "")
	v.SetDefault("tls.acme.enabled", false)
	v.SetDefault("tls.acme.email", "")
	v.SetDefault("tls.acme.challenge", "auto")
	v.SetDefault("tls.acme.obtain_batch_size", 1)
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
	v.SetDefault("backups.enabled", false)
	v.SetDefault("backups.schedule", string(domain.ScheduleDaily))
	v.SetDefault("backups.storage_dir", "")
	v.SetDefault("backups.retention.hourly", 0)
	v.SetDefault("backups.retention.daily", 0)
	v.SetDefault("backups.retention.weekly", 0)
	v.SetDefault("backups.retention.monthly", 0)
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
