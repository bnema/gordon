package app

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	authhandler "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/adapters/in/http/registry"
	trafficadapter "github.com/bnema/gordon/internal/adapters/in/traffic"
	acmelego "github.com/bnema/gordon/internal/adapters/out/acmelego"
	acmestore "github.com/bnema/gordon/internal/adapters/out/acmestore"
	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/adapters/out/logwriter"
	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
	"github.com/bnema/gordon/internal/usecase/apptraffic"
	"github.com/bnema/gordon/internal/usecase/auth"
	"github.com/bnema/gordon/internal/usecase/backup"
	"github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/deployment"
	"github.com/bnema/gordon/internal/usecase/health"
	"github.com/bnema/gordon/internal/usecase/images"
	"github.com/bnema/gordon/internal/usecase/logs"
	pkiusecase "github.com/bnema/gordon/internal/usecase/pki"
	"github.com/bnema/gordon/internal/usecase/proxy"
	"github.com/bnema/gordon/internal/usecase/publictls"
	registrySvc "github.com/bnema/gordon/internal/usecase/registry"
	"github.com/bnema/gordon/internal/usecase/registrystate"
	volumesSvc "github.com/bnema/gordon/internal/usecase/volumes"
	"github.com/bnema/gordon/pkg/bytesize"
)

// services holds all the services used by the application.
type services struct {
	runtime               *docker.Runtime
	eventBus              *eventbus.InMemory
	metrics               *telemetry.Metrics
	blobStorage           *filesystem.BlobStorage
	manifestStorage       *filesystem.ManifestStorage
	backupStorage         *filesystem.BackupStorage
	volumeBackupStore     out.VolumeBackupStorage
	volumeBackupCfg       domain.VolumeBackupConfig
	logWriter             *logwriter.LogWriter
	tokenStore            out.TokenStore
	configSvc             *config.Service
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
	appSvcImpl           *apps.AppServiceImpl
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
	// workloadLogs exports proxy access and app container logs over
	// OTLP. Nil when telemetry log export is disabled.
	workloadLogs out.LogExporter
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

	runtime := &reloadRuntime{v: v, svc: si.svc, log: log}
	si.svc.reloadCoordinator = newReloadCoordinator(v, si.svc.configSvc, si.svc.proxySvc, nil, si.svc.eventBus, si.svc.publicTLSSvc, runtime.Apply, log)

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

func startPublicTLSRuntimeWithWarning(ctx context.Context, svc publicTLSRuntime, log zerowrap.Logger) {
	if err := startPublicTLSRuntime(ctx, svc, log); err != nil {
		log.Warn().Err(err).Msg("initial public ACME reconcile failed, continuing with renewal loop")
	}
}

// initSecrets validates the secrets backend and creates the app service
// secret provider.
func (si *serviceInit) initSecrets() error {
	backend, err := resolveSecretsBackend(si.cfg.Auth.SecretsBackend)
	if err != nil {
		return si.log.WrapErr(err, "failed to resolve secrets backend")
	}
	si.svc.serviceSecretProvider = createStandaloneServiceSecretProvider(backend, resolveDataDir(si.cfg.Server.DataDir), si.log)
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
	svc.metrics = gordonMetrics
}

// observeManagedContainers exports the managed-container gauge from app
// state. No-op when metrics are disabled.
func observeManagedContainers(metrics *telemetry.Metrics, state out.AppStateReader, log zerowrap.Logger) {
	if metrics == nil {
		return
	}
	if err := metrics.ObserveManagedContainers(func(ctx context.Context) (int64, error) {
		return apps.CountManagedContainers(ctx, state)
	}); err != nil {
		log.Warn().Err(err).Msg("failed to register managed container gauge")
	}
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

// proxyConfigResult holds parsed proxy and blob chunk size config.
type proxyConfigResult struct {
	proxyConfig      proxy.Config
	maxBlobChunkSize int64
	maxBlobSize      int64
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
	return container.NewService(svc.runtime, svc.eventBus, svc.logWriter, containerConfig), nil
}

// registerEventHandlers registers event handlers. Push-triggered
// route creation (auto-route, previews, image-pushed deploy) is
// retired: pushes transfer OCI content only and apps deploy
// explicitly via `gordon apps deploy`. The pre-v3 route-engine
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
		if svc.reloadCoordinator != nil {
			svc.reloadCoordinator.Stop()
		}
	}

	return cleanup, nil
}
