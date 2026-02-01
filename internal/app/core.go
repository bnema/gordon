// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	// gRPC adapters
	grpcadmin "github.com/bnema/gordon/internal/adapters/in/grpc/admin"
	grpcauth "github.com/bnema/gordon/internal/adapters/in/grpc/auth"
	grpccore "github.com/bnema/gordon/internal/adapters/in/grpc/core"
	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/adapters/out/envloader"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/adapters/out/httpprober"
	"github.com/bnema/gordon/internal/adapters/out/logwriter"
	"github.com/bnema/gordon/internal/adapters/out/ratelimit"

	// HTTP handlers
	authhandler "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/adapters/in/http/registry"

	// Use cases
	"github.com/bnema/gordon/internal/boundaries/out"
	gordon "github.com/bnema/gordon/internal/grpc"
	"github.com/bnema/gordon/internal/usecase/auth"
	"github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/health"
	"github.com/bnema/gordon/internal/usecase/logs"
	"github.com/bnema/gordon/internal/usecase/proxy"
	registrySvc "github.com/bnema/gordon/internal/usecase/registry"
	secretsSvc "github.com/bnema/gordon/internal/usecase/secrets"

	grpclib "google.golang.org/grpc"
	grpclibhealth "google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// coreServices holds the services specific to the core component.
type coreServices struct {
	runtime         *docker.Runtime
	eventBus        *eventbus.InMemory
	blobStorage     *filesystem.BlobStorage
	manifestStorage *filesystem.ManifestStorage
	envLoader       *envloader.FileLoader
	logWriter       *logwriter.LogWriter
	tokenStore      out.TokenStore
	configSvc       *config.Service
	containerSvc    *container.Service
	registrySvc     *registrySvc.Service
	proxySvc        *proxy.Service
	authSvc         *auth.Service
	authHandler     *authhandler.Handler
	healthSvc       *health.Service
	logSvc          *logs.Service
	secretSvc       *secretsSvc.Service
	lifecycle       *LifecycleManager
	internalRegUser string
	internalRegPass string
	envDir          string
	log             zerowrap.Logger
}

// RunCore starts the gordon-core component.
// This is the orchestrator component that:
//   - Has Docker socket access
//   - Provides CoreService gRPC on :9090
//   - Deploys and manages other sub-containers
func RunCore(ctx context.Context, configPath string) error {
	// Load configuration
	v, cfg, err := initConfig(configPath)
	if err != nil {
		return err
	}
	_ = v

	// Initialize logger
	log, cleanup, err := initLogger(cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	ctx = zerowrap.WithCtx(ctx, log)
	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Msg("starting gordon-core component")

	// Create PID file
	pidFile := createPidFile(log)
	if pidFile != "" {
		defer removePidFile(pidFile, log)
	}

	// Create core services
	svc, err := createCoreServices(ctx, v, cfg, log)
	if err != nil {
		return err
	}

	// Start event bus
	if err := svc.eventBus.Start(); err != nil {
		return log.WrapErr(err, "failed to start event bus")
	}
	defer svc.eventBus.Stop()

	// Register event handlers for auto-deploy on push
	if err := registerCoreEventHandlers(ctx, svc, cfg); err != nil {
		return err
	}

	// Sync and auto-start containers
	syncAndAutoStartContainers(ctx, svc, log)

	// Deploy and manage sub-containers (gordon-secrets, gordon-registry, gordon-proxy)
	if svc.lifecycle != nil {
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "core").
			Msg("deploying sub-containers")

		if err := svc.lifecycle.DeployAll(ctx); err != nil {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "core").
				Err(err).
				Msg("failed to deploy sub-containers, continuing anyway")
		} else {
			// Start monitoring loop in background
			go svc.lifecycle.MonitorLoop(ctx)
			log.Info().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "core").
				Msg("sub-container monitoring started")
		}
	}

	// Create gRPC server
	grpcPort := getEnvOrDefault("GORDON_CORE_GRPC_PORT", "9090")
	grpcLis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		return fmt.Errorf("failed to listen on gRPC port %s: %w", grpcPort, err)
	}

	grpcServer := grpclib.NewServer(
		grpclib.UnaryInterceptor(grpcauth.UnaryInterceptor(svc.authSvc)),
		grpclib.StreamInterceptor(grpcauth.StreamInterceptor(svc.authSvc)),
	)
	coreServer := grpccore.NewServer(
		svc.containerSvc,
		svc.configSvc,
		svc.runtime,
		svc.eventBus,
		log,
	)
	gordon.RegisterCoreServiceServer(grpcServer, coreServer)

	adminServer := grpcadmin.NewServer(
		svc.configSvc,
		svc.authSvc,
		svc.containerSvc,
		svc.healthSvc,
		svc.secretSvc,
		svc.logSvc,
		svc.registrySvc,
		svc.eventBus,
		log,
	)
	gordon.RegisterAdminServiceServer(grpcServer, adminServer)

	// Register health check
	healthServer := grpclibhealth.NewServer()
	grpc_health_v1.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Str("port", grpcPort).
		Msg("gRPC server listening")

	// Start gRPC server in goroutine
	go func() {
		if err := grpcServer.Serve(grpcLis); err != nil {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "core").
				Err(err).
				Msg("gRPC server error")
		}
	}()

	// Create HTTP handlers for admin API and registry
	_, registryHandler := createCoreHTTPHandlers(svc, cfg, log)

	// Start admin API and registry servers
	if err := runCoreServers(ctx, cfg, nil, registryHandler, svc.eventBus, log); err != nil {
		return err
	}

	// Graceful shutdown
	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Msg("shutting down gRPC server")
	grpcServer.GracefulStop()

	// Shutdown managed containers
	if svc.containerSvc != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := svc.containerSvc.Shutdown(shutdownCtx); err != nil {
			log.Warn().Err(err).Msg("error during container shutdown")
		}
	}

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Msg("gordon-core shutdown complete")

	return nil
}

// createCoreServices creates the services needed for the core component.
func createCoreServices(ctx context.Context, v *viper.Viper, cfg Config, log zerowrap.Logger) (*coreServices, error) {
	svc := &coreServices{log: log}
	var err error

	// Create Docker runtime and event bus
	if svc.runtime, svc.eventBus, err = createOutputAdapters(ctx, log); err != nil {
		return nil, err
	}

	// Create lifecycle manager for sub-container orchestration
	selfImage := GetSelfImage(svc.runtime)
	svc.lifecycle = NewLifecycleManager(svc.runtime, selfImage, log)
	svc.lifecycle.InitializeSpecs(cfg)

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Str("self_image", selfImage).
		Msg("lifecycle manager initialized")

	// Create storage
	if svc.blobStorage, svc.manifestStorage, err = createStorage(cfg, log); err != nil {
		return nil, err
	}

	// Create env loader and log writer for container service
	if svc.envLoader, err = createEnvLoader(cfg, log); err != nil {
		return nil, err
	}

	if svc.logWriter, err = createLogWriter(cfg, log); err != nil {
		return nil, err
	}

	// Create auth service (if enabled)
	if svc.tokenStore, svc.authSvc, err = createAuthService(ctx, cfg, log); err != nil {
		return nil, err
	}

	// Generate internal registry credentials for loopback-only pulls
	if cfg.Auth.Enabled {
		svc.internalRegUser, svc.internalRegPass, err = generateInternalRegistryAuth()
		if err != nil {
			return nil, log.WrapErr(err, "failed to generate internal registry credentials")
		}

		// Persist credentials to file for CLI access
		if err := persistInternalCredentials(svc.internalRegUser, svc.internalRegPass); err != nil {
			log.Warn().Err(err).Msg("failed to persist internal credentials for CLI access")
		}

		log.Debug().Msg("internal registry auth generated for loopback pulls")
	}

	// Create use case services
	svc.configSvc = config.NewService(v, svc.eventBus)
	if err := svc.configSvc.Load(ctx); err != nil {
		return nil, log.WrapErr(err, "failed to load configuration")
	}

	svc.containerSvc = createCoreContainerService(v, cfg, svc)
	svc.registrySvc = registrySvc.NewService(svc.blobStorage, svc.manifestStorage, svc.eventBus)
	svc.proxySvc = proxy.NewService(svc.runtime, svc.containerSvc, svc.configSvc, proxy.Config{
		RegistryDomain: cfg.Server.RegistryDomain,
		RegistryPort:   cfg.Server.RegistryPort,
	})

	// Create token handler for registry token endpoint
	if svc.authSvc != nil {
		internalAuth := authhandler.InternalAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		svc.authHandler = authhandler.NewHandler(svc.authSvc, internalAuth, log)
	}

	// Determine env directory for admin API
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	svc.envDir = cfg.Env.Dir
	if svc.envDir == "" {
		svc.envDir = filepath.Join(dataDir, "env")
	}

	// Create domain secret store and service
	domainSecretStore, err := domainsecrets.NewFileStore(svc.envDir, log)
	if err != nil {
		return nil, log.WrapErr(err, "failed to create domain secret store")
	}
	svc.secretSvc = secretsSvc.NewService(domainSecretStore, log)

	// Create health service for route health checking
	prober := httpprober.New()
	svc.healthSvc = health.NewService(svc.configSvc, svc.containerSvc, prober, log)

	// Create log service for accessing logs via admin API
	svc.logSvc = logs.NewService(resolveLogFilePath(cfg), svc.containerSvc, svc.runtime, log)

	// Create admin handler for admin API
	return svc, nil
}

// createCoreHTTPHandlers creates the HTTP handlers for core component.
func createCoreHTTPHandlers(svc *coreServices, cfg Config, log zerowrap.Logger) (http.Handler, http.Handler) {
	// Registry handler with middleware
	registryHandler := registry.NewHandler(svc.registrySvc, log)
	registryMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log),
		middleware.SecurityHeaders,
	}

	// Create rate limiters using the factory
	var rateLimitMiddleware func(http.Handler) http.Handler
	if cfg.API.RateLimit.Enabled {
		globalLimiter := ratelimit.NewMemoryStore(cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.Burst, log)
		ipLimiter := ratelimit.NewMemoryStore(cfg.API.RateLimit.PerIPRPS, cfg.API.RateLimit.Burst, log)
		rateLimitMiddleware = registry.RateLimitMiddleware(
			globalLimiter,
			ipLimiter,
			cfg.API.RateLimit.TrustedProxies,
			log,
		)
	} else {
		rateLimitMiddleware = registry.RateLimitMiddleware(nil, nil, nil, log)
	}
	registryMiddlewares = append(registryMiddlewares, rateLimitMiddleware)

	// Add auth middleware if enabled
	if cfg.Auth.Enabled && svc.authSvc != nil {
		internalAuth := middleware.InternalRegistryAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		registryMiddlewares = append(registryMiddlewares,
			middleware.RegistryAuthV2(svc.authSvc, internalAuth, log))
	}

	registryWithMiddleware := middleware.Chain(registryMiddlewares...)(registryHandler)

	return nil, registryWithMiddleware
}

// runCoreServers starts the HTTP servers for core component.
func runCoreServers(ctx context.Context, cfg Config, adminHandler, registryHandler http.Handler, eventBus out.EventBus, log zerowrap.Logger) error {
	mux := http.NewServeMux()

	// Mount registry at /v2/ (Docker registry API)
	mux.Handle("/v2/", registryHandler)

	// Add health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy","component":"core"}`))
	})

	// Server configuration
	adminPort := cfg.Server.Port
	if adminPort == 0 {
		adminPort = 5000
	}

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", adminPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Int("port", adminPort).
		Msg("admin API server listening")

	// Start server in goroutine
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().
				Str(zerowrap.FieldLayer, "app").
				Str(zerowrap.FieldComponent, "core").
				Err(err).
				Msg("admin API server error")
		}
	}()

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "core").
			Msg("context cancelled, shutting down")
	case sig := <-quit:
		log.Info().
			Str(zerowrap.FieldLayer, "app").
			Str(zerowrap.FieldComponent, "core").
			Str("signal", sig.String()).
			Msg("received shutdown signal")
	}

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	log.Info().
		Str(zerowrap.FieldLayer, "app").
		Str(zerowrap.FieldComponent, "core").
		Msg("shutting down admin API server")

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Warn().Err(err).Msg("admin API server shutdown error")
	}

	return nil
}

// registerCoreEventHandlers registers event handlers for the core component.
func registerCoreEventHandlers(ctx context.Context, svc *coreServices, cfg Config) error {
	// Create event handler for image pushed events
	imagePushedHandler := container.NewImagePushedHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(imagePushedHandler); err != nil {
		return fmt.Errorf("failed to subscribe image pushed handler: %w", err)
	}

	// Auto-route handler for creating routes from image labels
	autoRouteHandler := container.NewAutoRouteHandler(ctx, svc.configSvc, svc.containerSvc, svc.blobStorage, cfg.Server.RegistryDomain).
		WithEnvExtractor(svc.runtime, svc.envDir)
	if err := svc.eventBus.Subscribe(autoRouteHandler); err != nil {
		return fmt.Errorf("failed to subscribe auto-route handler: %w", err)
	}

	// Config reload handler
	configReloadHandler := container.NewConfigReloadHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(configReloadHandler); err != nil {
		return fmt.Errorf("failed to subscribe config reload handler: %w", err)
	}

	// Manual reload handler
	manualReloadHandler := container.NewManualReloadHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(manualReloadHandler); err != nil {
		return fmt.Errorf("failed to subscribe manual reload handler: %w", err)
	}

	// Manual deploy handler
	manualDeployHandler := container.NewManualDeployHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(manualDeployHandler); err != nil {
		return fmt.Errorf("failed to subscribe manual deploy handler: %w", err)
	}

	return nil
}

// syncAndAutoStartContainers syncs container state and auto-starts configured containers.
func syncAndAutoStartContainers(ctx context.Context, svc *coreServices, log zerowrap.Logger) {
	if err := svc.containerSvc.SyncContainers(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to sync existing containers")
	}

	if svc.configSvc.IsAutoRouteEnabled() {
		routes := svc.configSvc.GetRoutes(ctx)
		if err := svc.containerSvc.AutoStart(ctx, routes); err != nil {
			log.Warn().Err(err).Msg("failed to auto-start containers")
		}
	}
}

// createCoreContainerService creates the container service with configuration.
func createCoreContainerService(v *viper.Viper, cfg Config, svc *coreServices) *container.Service {
	containerConfig := container.Config{
		RegistryAuthEnabled:      cfg.Auth.Enabled,
		RegistryDomain:           cfg.Server.RegistryDomain,
		RegistryPort:             cfg.Server.RegistryPort,
		RegistryUsername:         cfg.Auth.Username,
		RegistryPassword:         cfg.Auth.Password,
		InternalRegistryUsername: svc.internalRegUser,
		InternalRegistryPassword: svc.internalRegPass,
		PullPolicy:               v.GetString("deploy.pull_policy"),
		VolumeAutoCreate:         v.GetBool("volumes.auto_create"),
		VolumePrefix:             v.GetString("volumes.prefix"),
		VolumePreserve:           v.GetBool("volumes.preserve"),
		NetworkIsolation:         v.GetBool("network_isolation.enabled"),
		NetworkPrefix:            v.GetString("network_isolation.network_prefix"),
		DNSSuffix:                v.GetString("network_isolation.dns_suffix"),
		NetworkGroups:            svc.configSvc.GetNetworkGroups(),
		Attachments:              svc.configSvc.GetAttachments(),
	}
	return container.NewService(svc.runtime, svc.envLoader, svc.eventBus, svc.logWriter, containerConfig)
}
