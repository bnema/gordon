// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	// Adapters - Output
	acmelego "github.com/bnema/gordon/internal/adapters/out/acmelego"
	acmestore "github.com/bnema/gordon/internal/adapters/out/acmestore"
	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/adapters/out/httpprober"
	"github.com/bnema/gordon/internal/adapters/out/logwriter"
	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/adapters/out/telemetry"

	// Adapters - Input
	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	authhandler "github.com/bnema/gordon/internal/adapters/in/http/auth"
	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"

	// Boundaries
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"

	// Domain
	"github.com/bnema/gordon/internal/domain"

	// Use cases
	"github.com/bnema/gordon/internal/usecase/auth"
	"github.com/bnema/gordon/internal/usecase/auto"
	"github.com/bnema/gordon/internal/usecase/auto/preview"
	"github.com/bnema/gordon/internal/usecase/backup"
	"github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	cronSvc "github.com/bnema/gordon/internal/usecase/cron"
	"github.com/bnema/gordon/internal/usecase/edgesnapshot"
	"github.com/bnema/gordon/internal/usecase/health"
	"github.com/bnema/gordon/internal/usecase/images"
	"github.com/bnema/gordon/internal/usecase/logs"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/gordon/internal/usecase/proxy"
	"github.com/bnema/gordon/internal/usecase/publictls"
	registrySvc "github.com/bnema/gordon/internal/usecase/registry"
	"github.com/bnema/gordon/internal/usecase/runtimecontrol"
	secretsSvc "github.com/bnema/gordon/internal/usecase/secrets"
	servicecfg "github.com/bnema/gordon/internal/usecase/services"
	"github.com/bnema/gordon/internal/usecase/traffic"
	volumesSvc "github.com/bnema/gordon/internal/usecase/volumes"
)

// Config holds the application configuration.
type ControlConfig struct {
	// ListenAddress is the control-role gRPC bind address. It is deliberately
	// separate from Endpoint, which is only the edge-role dial target.
	ListenAddress string `mapstructure:"listen_address"`
	Endpoint      string `mapstructure:"endpoint"`
	Token         string `mapstructure:"token"`
	TokenEnv      string `mapstructure:"token_env"`
	// InsecureTLS permits plaintext gRPC only when explicitly enabled. TLS with
	// normal hostname verification is the default for edge control connections.
	InsecureTLS bool `mapstructure:"insecure_tls"`
	// EdgeAlias is the split-network alias that runtime attachments must name.
	EdgeAlias string `mapstructure:"edge_alias"`
	// RegistryAlias and RegistryPort are the control-owned internal registry
	// target contract; neither accepts host loopback endpoints.
	RegistryAlias            string `mapstructure:"registry_alias"`
	RegistryPort             int    `mapstructure:"registry_port"`
	DrainRegistrationTimeout string `mapstructure:"drain_registration_timeout"`
	// HTTP is the management API listener. It is deliberately separate from
	// ListenAddress, which is reserved for component gRPC traffic.
	HTTP struct {
		ListenAddress string `mapstructure:"listen_address"`
		TLSCertFile   string `mapstructure:"tls_cert_file"`
		TLSKeyFile    string `mapstructure:"tls_key_file"`
		// InsecureTLS is an explicit private/test-only plaintext opt-in.
		InsecureTLS bool `mapstructure:"insecure_tls"`
	} `mapstructure:"http"`
}

type Config struct {
	Control ControlConfig        `mapstructure:"control"`
	Runtime RuntimeControlConfig `mapstructure:"runtime"`

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
	role                  Role
	runtime               *docker.Runtime
	runtimeDetection      docker.DetectionResult
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
	healthSvc             in.HealthService
	logSvc                *logs.Service
	imageSvc              *images.Service
	volumeSvc             *volumesSvc.Service
	proxySvc              *proxy.Service
	standaloneServiceSvc  in.StandaloneServiceService
	serviceSecretProvider out.SecretProvider
	authSvc               *auth.Service
	authHandler           *authhandler.Handler
	adminHandler          *admin.Handler
	httpProxyHandler      http.Handler
	httpsProxyHandler     http.Handler
	internalRegUser       string
	internalRegPass       string
	previewStore          *filesystem.PreviewStore
	previewService        *preview.Service
	envDir                string
	maxBlobChunkSize      int64
	maxBlobSize           int64
	caAdapter             *pkiadapter.CA
	pkiSvc                *pkiusecase.Service
	reloadCoordinator     *reloadCoordinator
	publicTLSSvc          in.PublicTLSService
	publicTLSRuntime      publicTLSRuntime
	runtimeCommandClient  out.RuntimeCommandClient
	runtimeControl        *runtimecontrol.Service
	migrationSvc          *MigrationService
	appliedStateTracker   *edgesnapshot.AppliedStateTracker
	appliedStateReceiver  edgesnapshot.AppliedStateReceiver
	trafficManager        *trafficadapter.Manager
	tlsHTTPEntryPoints    map[string]struct{}
	smartHTTPEntryPoints  map[string]struct{}
	registryHandler       interface {
		UpdateBlobLimits(maxBlobChunkSize, maxBlobSize int64)
	}
}

var (
	runMonolith = runMonolithImpl
	runControl  = runControlImpl
	runRuntime  = runRuntimeImpl
	runEdge     = runEdgeImpl
	runRegistry = runRegistryImpl
)

// Run initializes and starts the Gordon application in the default monolith role.
func Run(ctx context.Context, configPath string) error {
	return runMonolith(ctx, configPath)
}

// RunWithRole initializes and starts the Gordon application for the requested role.
func RunWithRole(ctx context.Context, configPath, roleValue string) error {
	role, err := ResolveRole(roleValue, os.Getenv("GORDON_ROLE"))
	if err != nil {
		return err
	}
	switch role {
	case RoleMonolith:
		return runMonolith(ctx, configPath)
	case RoleControl:
		return runControl(ctx, configPath)
	case RoleRuntime:
		return runRuntime(ctx, configPath)
	case RoleEdge:
		return runEdge(ctx, configPath)
	case RoleRegistry:
		return runRegistry(ctx, configPath)
	default:
		return fmt.Errorf("invalid role %q; accepted values: %s", role, acceptedRoleValues)
	}
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
		svc: &services{role: RoleMonolith},
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

	// Detect once and retain the exact selected endpoint for migration. The
	// runtime adapter and generated runtime role environment share this result.
	runtimeSocket := resolveRuntimeConfig(v.GetString("server.runtime"))
	si.svc.runtimeDetection = docker.DetectRuntimeSocket(runtimeSocket)
	if si.svc.runtime, si.svc.eventBus, err = createOutputAdaptersFromDetection(ctx, log, RoleMonolith, si.svc.runtimeDetection); err != nil {
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
	si.svc.pkiSvc = pkiusecase.NewService(si.ctx, caAdapter, si.svc.configSvc, []string{si.cfg.Server.GordonDomain}, si.log)
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
		Routes:          si.svc.configSvc,
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
	return nil
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
	if si.svc.runtimeCommandClient, err = createRuntimeCommandClient(si.ctx, si.cfg.Runtime); err != nil {
		return err
	}

	si.svc.registrySvc = registrySvc.NewService(si.svc.blobStorage, si.svc.manifestStorage, si.svc.eventBus)
	si.svc.imageSvc = images.NewServiceWithRuntimeImageManager(runtimeImageManagerForServices(si.svc), si.svc.manifestStorage, si.svc.blobStorage, si.log)
	si.svc.volumeSvc = volumesSvc.NewServiceWithRuntimeVolumeManager(runtimeVolumeManagerForServices(si.svc))

	injectTelemetryMetrics(si.cfg, si.svc, si.log)

	proxyCfg, err := buildProxyConfig(si.cfg, si.log)
	if err != nil {
		return err
	}
	si.svc.maxBlobChunkSize = proxyCfg.maxBlobChunkSize
	si.svc.maxBlobSize = proxyCfg.maxBlobSize
	var drainWaiter out.ProxyDrainWaiter
	si.svc.proxySvc, drainWaiter = newMonolithProxyServiceWithDrainWaiter(si.svc.runtime, si.svc.containerSvc, si.svc.configSvc, proxyCfg.proxyConfig)
	si.svc.standaloneServiceSvc = servicecfg.NewServiceWithRuntimeStandaloneServiceManagerAndSecretProvider(standaloneServiceManagerForServices(si.svc), si.svc.serviceSecretProvider)

	// Wire synchronous proxy cache invalidation for zero-downtime deployments.
	// The proxy service implements out.ProxyCacheInvalidator via InvalidateTarget().
	si.svc.containerSvc.SetProxyCacheInvalidator(si.svc.proxySvc)
	si.svc.containerSvc.SetProxyDrainWaiter(drainWaiter)
	return nil
}

// newMonolithProxyService explicitly wires the local route snapshot provider
// into the snapshot-first proxy service. Monolith snapshots may use loopback
// targets; split-edge reachability is validated by the edge role.
func newMonolithProxyService(runtime out.ContainerRuntime, containerSvc in.ContainerService, configSvc in.ConfigService, config proxy.Config) *proxy.Service {
	service, _ := newMonolithProxyServiceWithDrainWaiter(runtime, containerSvc, configSvc, config)
	return service
}

func newMonolithProxyServiceWithDrainWaiter(runtime out.ContainerRuntime, containerSvc in.ContainerService, configSvc in.ConfigService, config proxy.Config) (*proxy.Service, out.ProxyDrainWaiter) {
	localSnapshots := proxy.NewLocalSnapshotProvider(runtime, containerSvc, configSvc, config)
	service := proxy.NewSnapshotService(localSnapshots, config)
	return service, proxy.NewLocalSnapshotDrainWaiter(localSnapshots, service)
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
		if err := applyTrafficRuntimeConfig(reloadCtx, si.svc.trafficManager, reloadCfg, si.svc.configSvc); err != nil {
			return err
		}
		si.svc.tlsHTTPEntryPoints = registerTLSMuxHTTPServers(si.svc.trafficManager, reloadCfg, si.svc.httpsProxyHandler, tlsConfig, si.svc.tlsHTTPEntryPoints)
		si.svc.smartHTTPEntryPoints = registerSmartTCPHTTPServers(si.svc.trafficManager, reloadCfg, si.svc.httpProxyHandler, si.svc.httpsProxyHandler, tlsConfig, si.svc.smartHTTPEntryPoints)
		if err := reconcileStandaloneServices(reloadCtx, si.svc.standaloneServiceSvc, reloadCfg); err != nil {
			return err
		}
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

	prober := httpprober.New()
	si.svc.healthSvc = health.NewService(si.svc.configSvc, si.svc.containerSvc, prober, si.log)

	si.svc.logSvc = logs.NewServiceWithRuntimeLogReader(resolveLogFilePath(si.cfg), si.cfg.Logging.File.Enabled, si.svc.containerSvc, runtimeLogReaderForServices(si.svc), si.log)

	initRuntimeControlFacade(si.svc)
	initPreviewService(si.ctx, si.cfg, si.svc, si.log)
	if si.svc.trafficManager == nil {
		si.svc.trafficManager = trafficadapter.NewManager()
	}

	handlerDeps := admin.HandlerDeps{
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
		PreviewSvc:      si.svc.previewService,
		ImageSvc:        si.svc.imageSvc,
		VolumeSvc:       si.svc.volumeSvc,
		PublicTLSSvc:    si.svc.publicTLSSvc,
		TrafficSvc:      si.svc.trafficManager,
	}
	assignRuntimeControlHandlerDep(&handlerDeps, si.svc.runtimeControl)
	si.svc.adminHandler = admin.NewHandler(handlerDeps)
}

// assignRuntimeControlHandlerDep avoids converting a nil concrete facade to a
// non-nil interface. Monolith mode intentionally has no runtime-control facade
// and must use its local container cleanup path for route deletion.
func assignRuntimeControlHandlerDep(deps *admin.HandlerDeps, runtimeControlSvc *runtimecontrol.Service) {
	if deps != nil && runtimeControlSvc != nil {
		deps.RuntimeControl = runtimeControlSvc
	}
}

// standaloneServiceManagerForServices selects the narrow standalone-service runtime port.
// Control uses its RPC client when available; monolith keeps the local runtime adapter.
func standaloneServiceManagerForServices(svc *services) out.RuntimeStandaloneServiceManager {
	if svc == nil {
		return nil
	}
	switch svc.role {
	case RoleControl:
		manager, _ := svc.runtimeCommandClient.(out.RuntimeStandaloneServiceManager)
		return manager
	case RoleMonolith:
		return servicecfg.NewLocalRuntimeStandaloneServiceManager(svc.runtime)
	default:
		return nil
	}
}

func runtimeLogReaderForServices(svc *services) out.RuntimeLogReader {
	if svc == nil {
		return nil
	}
	if svc.role == RoleControl {
		if reader, ok := svc.runtimeCommandClient.(out.RuntimeLogReader); ok {
			return reader
		}
	}
	return logs.NewLocalRuntimeLogReader(svc.containerSvc, svc.runtime)
}

func runtimeVolumeManagerForServices(svc *services) out.RuntimeVolumeManager {
	if svc == nil {
		return nil
	}
	if svc.role == RoleControl {
		if manager, ok := svc.runtimeCommandClient.(out.RuntimeVolumeManager); ok {
			return manager
		}
	}
	return volumesSvc.NewLocalRuntimeVolumeManager(svc.runtime)
}

func runtimeImageManagerForServices(svc *services) out.RuntimeImageManager {
	if svc == nil {
		return nil
	}
	if svc.role == RoleControl {
		if manager, ok := svc.runtimeCommandClient.(out.RuntimeImageManager); ok {
			return manager
		}
	}
	return images.NewLocalRuntimeImageManager(svc.runtime)
}

func initRuntimeControlFacade(svc *services) {
	if svc == nil || svc.role != RoleControl || svc.runtimeControl != nil || svc.runtimeCommandClient == nil {
		return
	}
	svc.runtimeControl = runtimecontrol.NewService(svc.configSvc, svc.runtimeCommandClient, "gordon-control")
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

func resolveRegistryDomains(cfg Config) (string, []string) {
	registryDomain := cfg.Server.GordonDomain
	if registryDomain == "" {
		registryDomain = cfg.Server.RegistryDomain
	}
	return registryDomain, append([]string{}, cfg.Server.LegacyRegistryDomains...)
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

// registerEventHandlers registers all event handlers.
func registerEventHandlers(ctx context.Context, svc *services, cfg Config) (func(), error) {
	imagePushedHandler := container.NewImagePushedHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(imagePushedHandler); err != nil {
		return nil, fmt.Errorf("failed to subscribe image pushed handler: %w", err)
	}

	// Auto-route handler for creating routes from image labels
	registryDomain, legacyRegistryDomains := resolveRegistryDomains(cfg)
	autoRouteHandler := container.NewAutoRouteHandler(ctx, svc.configSvc, svc.containerSvc, svc.blobStorage, registryDomain, legacyRegistryDomains...).
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
			if _, err := svc.volumeBackupSvc.RunVolumeBackups(jobCtx, "", ""); err != nil {
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
