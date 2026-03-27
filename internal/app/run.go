// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	zerowrapotel "github.com/bnema/zerowrap/otel"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"

	// Adapters - Output
	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/adapters/out/envloader"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/adapters/out/httpprober"
	"github.com/bnema/gordon/internal/adapters/out/logwriter"
	"github.com/bnema/gordon/internal/adapters/out/ratelimit"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"

	// OTel
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	// Adapters - Input
	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	authhandler "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	proxyadapter "github.com/bnema/gordon/internal/adapters/in/http/proxy"
	"github.com/bnema/gordon/internal/adapters/in/http/registry"

	// Boundaries
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
	"github.com/bnema/gordon/internal/usecase/proxy"
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
		RegistryDomain       string   `mapstructure:"registry_domain"` // Deprecated: use gordon_domain
		TLSEnabled           bool     `mapstructure:"tls_enabled"`
		TLSPort              int      `mapstructure:"tls_port"`
		TLSCertFile          string   `mapstructure:"tls_cert_file"`
		TLSKeyFile           string   `mapstructure:"tls_key_file"`
		DataDir              string   `mapstructure:"data_dir"`
		MaxProxyBodySize     string   `mapstructure:"max_proxy_body_size"`     // e.g., "512MB", "1GB"
		MaxBlobChunkSize     string   `mapstructure:"max_blob_chunk_size"`     // e.g., "512MB", "1GB"
		MaxProxyResponseSize string   `mapstructure:"max_proxy_response_size"` // e.g., "1GB", "0" for no limit
		MaxConcurrentConns   int      `mapstructure:"max_concurrent_connections"`
		RegistryAllowedIPs   []string `mapstructure:"registry_allowed_ips"`
		ProxyAllowedIPs      []string `mapstructure:"proxy_allowed_ips"`
		RegistryListenAddr   string   `mapstructure:"registry_listen_address"`
		ForceHSTS            bool     `mapstructure:"force_hsts"`
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
		Prune struct {
			Enabled  bool   `mapstructure:"enabled"`
			Schedule string `mapstructure:"schedule"`
			KeepLast int    `mapstructure:"keep_last"`
		} `mapstructure:"prune"`
	} `mapstructure:"images"`

	Containers struct {
		MemoryLimit string  `mapstructure:"memory_limit"` // e.g., "512MB", "1GB"
		CPULimit    float64 `mapstructure:"cpu_limit"`    // CPU cores, e.g., 1.0 = 1 core
		PidsLimit   int64   `mapstructure:"pids_limit"`   // e.g., 512
	} `mapstructure:"containers"`

	Telemetry telemetry.Config `mapstructure:"telemetry"`
}

// services holds all the services used by the application.
type services struct {
	runtime          *docker.Runtime
	eventBus         *eventbus.InMemory
	blobStorage      *filesystem.BlobStorage
	manifestStorage  *filesystem.ManifestStorage
	backupStorage    *filesystem.BackupStorage
	envLoader        out.EnvLoader
	logWriter        *logwriter.LogWriter
	tokenStore       out.TokenStore
	configSvc        *config.Service
	secretSvc        *secretsSvc.Service
	containerSvc     *container.Service
	backupSvc        *backup.Service
	registrySvc      *registrySvc.Service
	healthSvc        *health.Service
	logSvc           *logs.Service
	imageSvc         *images.Service
	volumeSvc        *volumesSvc.Service
	proxySvc         *proxy.Service
	authSvc          *auth.Service
	authHandler      *authhandler.Handler
	adminHandler     *admin.Handler
	internalRegUser  string
	internalRegPass  string
	previewStore     *filesystem.PreviewStore
	previewService   *preview.Service
	envDir           string
	maxBlobChunkSize int64
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

	if err := ensureTLSConfig(&cfg, log); err != nil {
		return err
	}

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
	setupConfigHotReload(ctx, v, svc, log)

	// Start event bus
	if err := svc.eventBus.Start(); err != nil {
		return log.WrapErr(err, "failed to start event bus")
	}
	defer svc.eventBus.Stop()

	// Create HTTP handlers
	registryHandler, proxyHandler := createHTTPHandlers(svc, cfg, log)

	// Start servers, wait for listeners to bind, then sync/auto-start containers.
	return runServers(ctx, v, cfg, registryHandler, proxyHandler, svc, cleanupHandlers, log)
}

func ensureTLSConfig(cfg *Config, log zerowrap.Logger) error {
	if !cfg.Server.TLSEnabled {
		return nil
	}

	if cfg.Server.TLSCertFile != "" && cfg.Server.TLSKeyFile != "" {
		return nil
	}

	dataDir := resolveDataDir(cfg.Server.DataDir)
	tlsDir := filepath.Join(dataDir, "tls")
	certPath := filepath.Join(tlsDir, "cert.pem")
	keyPath := filepath.Join(tlsDir, "key.pem")

	domainName := cfg.Server.GordonDomain
	if domainName == "" {
		domainName = cfg.Server.RegistryDomain
	}
	if domainName == "" {
		domainName = "localhost"
	}

	if err := generateSelfSignedTLSCert(certPath, keyPath, domainName); err != nil {
		return fmt.Errorf("failed to generate self-signed TLS certificate: %w", err)
	}

	cfg.Server.TLSCertFile = certPath
	cfg.Server.TLSKeyFile = keyPath

	log.Warn().
		Str("cert_file", certPath).
		Str("key_file", keyPath).
		Str("domain", domainName).
		Msg("server.tls_enabled=true with empty tls_cert_file/tls_key_file; using auto-generated self-signed certificate")

	return nil
}

func generateSelfSignedTLSCert(certPath, keyPath, domainName string) error {
	// Reuse existing pair if already generated.
	if _, err := os.Stat(certPath); err == nil {
		if _, keyErr := os.Stat(keyPath); keyErr == nil {
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return err
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	now := time.Now()
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return err
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName:   domainName,
			Organization: []string{"Gordon Auto-Generated TLS"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	if ip := net.ParseIP(domainName); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{domainName}
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return err
	}

	certOut, err := os.OpenFile(certPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer certOut.Close()

	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		return err
	}

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return err
	}

	keyOut, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer keyOut.Close()

	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}); err != nil {
		return err
	}

	return nil
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

	// Normalize domain config: prefer gordon_domain over registry_domain
	if cfg.Server.GordonDomain != "" {
		cfg.Server.RegistryDomain = cfg.Server.GordonDomain
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

// createServices creates all the application services.
func createServices(ctx context.Context, v *viper.Viper, cfg Config, log zerowrap.Logger) (*services, error) {
	svc := &services{}
	var err error

	// Create output adapters
	runtimeSocket := resolveRuntimeConfig(v.GetString("server.runtime"))
	if svc.runtime, svc.eventBus, err = createOutputAdapters(ctx, log, runtimeSocket); err != nil {
		return nil, err
	}

	// Create storage
	if svc.blobStorage, svc.manifestStorage, err = createStorage(cfg, log); err != nil {
		return nil, err
	}

	// Create log writer
	if svc.logWriter, err = createLogWriter(cfg, log); err != nil {
		return nil, err
	}

	// Create auth service (if enabled)
	if svc.tokenStore, svc.authSvc, err = createAuthService(ctx, cfg, log); err != nil {
		return nil, err
	}

	if err := setupInternalRegistryAuth(svc, log); err != nil {
		return nil, err
	}

	// Create use case services
	svc.configSvc = config.NewService(v, svc.eventBus)
	if err := svc.configSvc.Load(ctx); err != nil {
		return nil, log.WrapErr(err, "failed to load configuration")
	}

	var backend domain.SecretsBackend
	var passStore *domainsecrets.PassStore
	var domainSecretStore out.DomainSecretStore

	svc.envDir, backend, passStore, domainSecretStore, err = createDomainSecretStore(cfg, log)
	if err != nil {
		return nil, err
	}

	if svc.envLoader, err = createEnvLoader(backend, svc.envDir, passStore, log); err != nil {
		return nil, err
	}

	svc.secretSvc = secretsSvc.NewService(domainSecretStore, log, svc.eventBus)

	if svc.containerSvc, err = createContainerService(ctx, v, cfg, svc, log); err != nil {
		return nil, err
	}

	if svc.backupStorage, svc.backupSvc, err = createBackupService(cfg, svc, log); err != nil {
		return nil, err
	}

	svc.registrySvc = registrySvc.NewService(svc.blobStorage, svc.manifestStorage, svc.eventBus)
	svc.imageSvc = images.NewService(svc.runtime, svc.manifestStorage, svc.blobStorage, log)
	svc.volumeSvc = volumesSvc.NewService(svc.runtime)

	injectTelemetryMetrics(cfg, svc, log)

	proxyCfg, err := buildProxyConfig(cfg, log)
	if err != nil {
		return nil, err
	}
	svc.maxBlobChunkSize = proxyCfg.maxBlobChunkSize
	svc.proxySvc = proxy.NewService(svc.runtime, svc.containerSvc, svc.configSvc, proxyCfg.proxyConfig)

	// Wire synchronous proxy cache invalidation for zero-downtime deployments.
	// The proxy service implements out.ProxyCacheInvalidator via InvalidateTarget().
	svc.containerSvc.SetProxyCacheInvalidator(svc.proxySvc)
	svc.containerSvc.SetProxyDrainWaiter(svc.proxySvc)

	// Create token handler for registry token endpoint
	if svc.authSvc != nil {
		internalAuth := authhandler.InternalAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		svc.authHandler = authhandler.NewHandler(svc.authSvc, internalAuth, log)
	}

	// Create health service for route health checking
	prober := httpprober.New()
	svc.healthSvc = health.NewService(svc.configSvc, svc.containerSvc, prober, log)

	// Create log service for accessing logs via admin API
	svc.logSvc = logs.NewService(resolveLogFilePath(cfg), cfg.Logging.File.Enabled, svc.containerSvc, svc.runtime, log)

	initPreviewService(ctx, cfg, svc, log)

	// Create admin handler for admin API
	svc.adminHandler = admin.NewHandler(admin.HandlerDeps{
		ConfigSvc:    svc.configSvc,
		AuthSvc:      svc.authSvc,
		ContainerSvc: svc.containerSvc,
		HealthSvc:    svc.healthSvc,
		SecretSvc:    svc.secretSvc,
		LogSvc:       svc.logSvc,
		RegistrySvc:  svc.registrySvc,
		EventBus:     svc.eventBus,
		Log:          log,
		BackupSvc:    svc.backupSvc,
		PreviewSvc:   svc.previewService,
		ImageSvc:     svc.imageSvc,
		VolumeSvc:    svc.volumeSvc,
	})

	return svc, nil
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
	sweepInterval := previewConfig.TTL / 2
	if sweepInterval > 1*time.Hour {
		sweepInterval = 1 * time.Hour
	}
	if sweepInterval < 1*time.Minute {
		sweepInterval = 1 * time.Minute
	}

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
// "auto" or "" means auto-detect; any other value is treated as an explicit socket path.
// URI schemes (unix://, tcp://) are stripped so callers receive a bare path.
func resolveRuntimeConfig(value string) string {
	if value == "" || value == "auto" {
		return ""
	}
	if strings.HasPrefix(value, "unix://") {
		return strings.TrimPrefix(value, "unix://")
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
		// For unsafe backend, path is relative to dataDir/secrets/
		secretFile := filepath.Join(dataDir, "secrets", path)
		data, err := os.ReadFile(secretFile)
		if err != nil {
			return "", fmt.Errorf("failed to read secret file: %w", err)
		}
		return string(data), nil
	default:
		return "", fmt.Errorf("unknown secrets backend: %s", backend)
	}
}

// proxyConfigResult holds parsed proxy and blob chunk size config.
type proxyConfigResult struct {
	proxyConfig      proxy.Config
	maxBlobChunkSize int64
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
			RegistryDomain:     cfg.Server.RegistryDomain,
			RegistryPort:       cfg.Server.RegistryPort,
			MaxBodySize:        maxProxyBodySize,
			MaxResponseSize:    maxProxyResponseSize,
			MaxConcurrentConns: maxConcurrentConns,
		},
		maxBlobChunkSize: maxBlobChunkSize,
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

	containerConfig := container.Config{
		RegistryAuthEnabled:        cfg.Auth.Enabled,
		RegistryDomain:             cfg.Server.RegistryDomain,
		RegistryPort:               cfg.Server.RegistryPort,
		InternalRegistryUsername:   svc.internalRegUser,
		InternalRegistryPassword:   svc.internalRegPass,
		PullPolicy:                 v.GetString("deploy.pull_policy"),
		VolumeAutoCreate:           v.GetBool("volumes.auto_create"),
		VolumePrefix:               v.GetString("volumes.prefix"),
		VolumePreserve:             v.GetBool("volumes.preserve"),
		NetworkIsolation:           v.GetBool("network_isolation.enabled"),
		NetworkPrefix:              v.GetString("network_isolation.network_prefix"),
		NetworkGroups:              svc.configSvc.GetNetworkGroups(),
		Attachments:                svc.configSvc.GetAttachments(),
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
	autoRouteHandler := container.NewAutoRouteHandler(ctx, svc.configSvc, svc.containerSvc, svc.blobStorage, cfg.Server.RegistryDomain).
		WithEnvExtractor(svc.runtime, svc.envDir)

	// Preview handler for creating preview environments from tagged images
	autoPreviewHandler := preview.NewAutoPreviewHandler(
		ctx,
		svc.configSvc,
		svc.blobStorage,
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

	manualReloadHandler := container.NewManualReloadHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(manualReloadHandler); err != nil {
		return nil, fmt.Errorf("failed to subscribe manual reload handler: %w", err)
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

// setupConfigHotReload sets up Viper config hot reload.
// NOTE: This does NOT reload routes into memory. Routes are managed via API
// (AddRoute/UpdateRoute/RemoveRoute) and memory is the source of truth.
// The file watcher only updates proxy config and refreshes targets.
// Manual config file edits to routes require a server restart.
func setupConfigHotReload(ctx context.Context, v *viper.Viper, svc *services, log zerowrap.Logger) {
	v.OnConfigChange(func(e fsnotify.Event) {
		log.Info().Str("file", e.Name).Msg("config file changed")

		if err := v.ReadInConfig(); err != nil {
			log.Error().Err(err).Msg("failed to reload config")
			return
		}

		// Re-unmarshal config and rebuild proxy config to pick up all changes
		var reloadCfg Config
		if err := v.Unmarshal(&reloadCfg); err != nil {
			log.Error().Err(err).Msg("failed to unmarshal config on reload")
			return
		}
		if reloadCfg.Server.GordonDomain != "" {
			reloadCfg.Server.RegistryDomain = reloadCfg.Server.GordonDomain
		}
		reloadedProxy, err := buildProxyConfig(reloadCfg, log)
		if err != nil {
			log.Error().Err(err).Msg("failed to parse proxy config on reload")
			return
		}
		svc.proxySvc.UpdateConfig(reloadedProxy.proxyConfig)

		// Clear proxy target cache to pick up external route changes
		if err := svc.proxySvc.RefreshTargets(ctx); err != nil {
			log.Warn().Err(err).Msg("failed to refresh proxy targets")
		}

		log.Debug().Msg("config hot reload complete (routes unchanged, memory is source of truth)")
	})
	v.WatchConfig()
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
func createHTTPHandlers(svc *services, cfg Config, log zerowrap.Logger) (http.Handler, http.Handler) {
	// Parse trusted proxies once for all middleware chains.
	// This ensures consistent IP extraction across logging, rate limiting, and auth.
	trustedNets := middleware.ParseTrustedProxies(cfg.API.RateLimit.TrustedProxies)

	registryHandler := registry.NewHandler(svc.registrySvc, log, svc.maxBlobChunkSize)
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

	// SECURITY: No CORS middleware on the proxy chain. Backend applications
	// should control their own CORS policies. A blanket Access-Control-Allow-Origin: *
	// would override backend CORS settings and allow any website to make
	// cross-origin authenticated requests to proxied applications.
	proxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeadersWithOptions(cfg.Server.ForceHSTS),
	}

	// SECURITY: When proxy_allowed_ips is set, only connections from those CIDRs
	// can reach the proxy. This prevents direct-to-origin attacks that bypass
	// CDN protections (e.g. Cloudflare WAF, DDoS mitigation).
	if proxyCIDRMiddleware := buildProxyCIDRAllowlistMiddleware(cfg, trustedNets, log); proxyCIDRMiddleware != nil {
		proxyMiddlewares = append(proxyMiddlewares, proxyCIDRMiddleware)
	}

	proxyHandler := proxyadapter.NewHandler(svc.proxySvc, log)
	proxyWithMiddleware := otelhttp.NewHandler(
		middleware.Chain(proxyMiddlewares...)(proxyHandler),
		"gordon.proxy",
	)
	proxyMux := http.NewServeMux()
	proxyMux.Handle("/", proxyWithMiddleware)

	registerAdminRoutes(registryMux, svc, cfg, trustedNets, log)

	return registryMux, proxyMux
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

	appendRegistryAuthMiddleware(&registryMiddlewares, svc, cfg, log)

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

	allowedNets := middleware.ParseTrustedProxies(ips)
	if len(allowedNets) != len(ips) {
		for _, entry := range ips {
			if nets := middleware.ParseTrustedProxies([]string{entry}); len(nets) == 0 {
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

func buildProxyCIDRAllowlistMiddleware(cfg Config, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	allowedNets, allInvalid := parseCIDRAllowlist(cfg.Server.ProxyAllowedIPs, "proxy_allowed_ips", log)
	if allInvalid {
		return denyAllHandler("proxy_allowed_ips", trustedNets, log)
	}
	if allowedNets == nil {
		return nil
	}

	log.Info().
		Strs("proxy_allowed_ips", cfg.Server.ProxyAllowedIPs).
		Msg("proxy origin IP allowlist enabled")

	return middleware.ProxyCIDRAllowlist(allowedNets, log)
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

func appendRegistryAuthMiddleware(registryMiddlewares *[]func(http.Handler) http.Handler, svc *services, cfg Config, log zerowrap.Logger) {
	if svc.authSvc != nil {
		internalAuth := middleware.InternalRegistryAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		*registryMiddlewares = append(*registryMiddlewares, middleware.RegistryAuthV2(svc.authSvc, internalAuth, log))
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
func runServers(ctx context.Context, v *viper.Viper, cfg Config, registryHandler, proxyHandler http.Handler, svc *services, cleanupHandlers func(), log zerowrap.Logger) error {
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

	registryAddr := fmt.Sprintf("%s:%d", cfg.Server.RegistryListenAddr, cfg.Server.RegistryPort)
	registrySrv, registryReady := startServer(registryAddr, registryHandler, "registry", nil, errChan, log)

	proxySrv, proxyReady, tlsSrv, tlsReady, err := startProxyServers(cfg, proxyHandler, errChan, log)
	if err != nil {
		return err
	}

	// Wait for all enabled servers to bind their ports before auto-starting containers.
	// This prevents the race where auto-start pulls from the registry before it's listening.
	if err := waitForServerReady(registryReady, errChan); err != nil {
		return err
	}
	if err := waitForServerReady(proxyReady, errChan); err != nil {
		return err
	}
	if cfg.Server.TLSEnabled {
		if err := waitForServerReady(tlsReady, errChan); err != nil {
			return err
		}
	}

	logEvent := log.Info().
		Int("proxy_port", cfg.Server.Port).
		Int("registry_port", cfg.Server.RegistryPort)
	if cfg.Server.TLSEnabled {
		logEvent = logEvent.Int("tls_port", cfg.Server.TLSPort)
	}
	logEvent.Msg("Gordon is running")

	schedulerCleanup, err := startOptionalSchedulers(ctx, cfg, svc, log, v)
	if err != nil {
		return err
	}
	if schedulerCleanup != nil {
		defer schedulerCleanup()
	}

	// Auto-start after servers are listening (registry port is now bound).
	syncAndAutoStart(ctx, svc, log)

	waitForShutdown(ctx, errChan, reloadChan, deployChan, svc.eventBus, log)
	cleanupHandlers() // Stop debounce timers before draining containers
	gracefulShutdown(registrySrv, proxySrv, tlsSrv, svc.containerSvc, svc.proxySvc, log)
	return nil
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
func waitForShutdown(ctx context.Context, errChan <-chan error, reloadChan, deployChan <-chan os.Signal, eventBus out.EventBus, log zerowrap.Logger) {
	for {
		select {
		case err := <-errChan:
			log.Error().Err(err).Msg("server error")
			return
		case <-reloadChan:
			log.Info().Msg("reload signal received (SIGUSR1)")
			if err := eventBus.Publish(domain.EventManualReload, nil); err != nil {
				log.Error().Err(err).Msg("failed to publish manual reload event")
			}
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
func gracefulShutdown(registrySrv, proxySrv, tlsSrv *http.Server, containerSvc *container.Service, proxySvc *proxy.Service, log zerowrap.Logger) {
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

// startProxyServers sets up proxy server(s) depending on TLS configuration.
// With TLS enabled, the HTTP port becomes a redirect-only server and a separate TLS server handles traffic.
// Without TLS, a single server handles both HTTP/1.1 and cleartext HTTP/2.
func startProxyServers(cfg Config, proxyHandler http.Handler, errChan chan<- error, log zerowrap.Logger) (*http.Server, <-chan struct{}, *http.Server, <-chan struct{}, error) {
	if !cfg.Server.TLSEnabled {
		var proxyProtos http.Protocols
		proxyProtos.SetHTTP1(true)
		proxyProtos.SetUnencryptedHTTP2(true)
		srv, ready := startServer(fmt.Sprintf(":%d", cfg.Server.Port), proxyHandler, "proxy", &proxyProtos, errChan, log)
		return srv, ready, nil, nil, nil
	}

	if cfg.Server.TLSCertFile == "" || cfg.Server.TLSKeyFile == "" {
		return nil, nil, nil, nil, fmt.Errorf("server.tls_enabled=true requires both server.tls_cert_file and server.tls_key_file")
	}

	tlsPort := cfg.Server.TLSPort
	redirectHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		} else if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		}
		if tlsPort != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(tlsPort))
		} else if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		target := "https://" + host + r.RequestURI
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})

	proxySrv, proxyReady := startServer(fmt.Sprintf(":%d", cfg.Server.Port), redirectHandler, "proxy-redirect", nil, errChan, log)
	tlsSrv, tlsReady := startTLSServer(
		fmt.Sprintf(":%d", cfg.Server.TLSPort),
		proxyHandler,
		"proxy-tls",
		cfg.Server.TLSCertFile,
		cfg.Server.TLSKeyFile,
		errChan,
		log,
	)
	return proxySrv, proxyReady, tlsSrv, tlsReady, nil
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

// startTLSServer starts an HTTPS server with the provided certificate and key.
// It returns the server instance (for graceful shutdown) and a channel that closes once the port is bound.
func startTLSServer(addr string, handler http.Handler, name, certFile, keyFile string, errChan chan<- error, log zerowrap.Logger) (*http.Server, <-chan struct{}) {
	ready := make(chan struct{})

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	go func() {
		log.Info().
			Str("address", addr).
			Str("cert_file", certFile).
			Str("key_file", keyFile).
			Msgf("%s server starting", name)

		ln, err := net.Listen("tcp", addr)
		if err != nil {
			errChan <- fmt.Errorf("%s server error: %w", name, err)
			return
		}
		close(ready)

		if err := server.ServeTLS(ln, certFile, keyFile); err != nil && err != http.ErrServerClosed {
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
		if err := os.WriteFile(location, []byte(fmt.Sprintf("%d", pid)), 0600); err == nil {
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
	v.SetDefault("server.port", 80)
	v.SetDefault("server.registry_port", 5000)
	v.SetDefault("server.tls_enabled", false)
	v.SetDefault("server.tls_port", 443)
	v.SetDefault("server.tls_cert_file", "")
	v.SetDefault("server.tls_key_file", "")
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
	v.SetDefault("images.prune.enabled", false)
	v.SetDefault("images.prune.schedule", string(domain.ScheduleDaily))
	v.SetDefault("images.prune.keep_last", domain.DefaultImagePruneKeepLast)
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
	v.SetDefault("server.force_hsts", false)
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
