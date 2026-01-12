// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"

	// Adapters - Output
	"gordon/internal/adapters/out/docker"
	"gordon/internal/adapters/out/envloader"
	"gordon/internal/adapters/out/eventbus"
	"gordon/internal/adapters/out/filesystem"
	"gordon/internal/adapters/out/logwriter"
	"gordon/internal/adapters/out/secrets"

	// Adapters - Input
	"gordon/internal/adapters/in/http/middleware"
	"gordon/internal/adapters/in/http/registry"

	// Use cases
	"gordon/internal/usecase/config"
	"gordon/internal/usecase/container"
	"gordon/internal/usecase/proxy"
	registrySvc "gordon/internal/usecase/registry"
)

// Config holds the application configuration.
type Config struct {
	Server struct {
		Port           int    `mapstructure:"port"`
		RegistryPort   int    `mapstructure:"registry_port"`
		RegistryDomain string `mapstructure:"registry_domain"`
		DataDir        string `mapstructure:"data_dir"`
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

	RegistryAuth struct {
		Enabled  bool   `mapstructure:"enabled"`
		Username string `mapstructure:"username"`
		Password string `mapstructure:"password"`
	} `mapstructure:"registry_auth"`
}

// services holds all the services used by the application.
type services struct {
	runtime         *docker.Runtime
	eventBus        *eventbus.InMemory
	blobStorage     *filesystem.BlobStorage
	manifestStorage *filesystem.ManifestStorage
	envLoader       *envloader.FileLoader
	logWriter       *logwriter.LogWriter
	configSvc       *config.Service
	containerSvc    *container.Service
	registrySvc     *registrySvc.Service
	proxySvc        *proxy.Service
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
	log.Info().Msg("Gordon starting")

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
	if err := registerEventHandlers(ctx, svc); err != nil {
		return err
	}

	// Set up config hot reload
	setupConfigHotReload(ctx, v, svc, log)

	// Start event bus
	if err := svc.eventBus.Start(); err != nil {
		return log.WrapErr(err, "failed to start event bus")
	}
	defer svc.eventBus.Stop()

	// Sync and auto-start containers
	syncAndAutoStart(ctx, svc, log)

	// Create HTTP handlers
	registryHandler, proxyHandler := createHTTPHandlers(svc, cfg, log)

	// Start servers and wait for shutdown
	return runServers(ctx, cfg, registryHandler, proxyHandler, svc.containerSvc, log)
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

	if cfg.Logging.File.Enabled {
		log, cleanup, err := zerowrap.NewWithFile(logConfig, zerowrap.FileConfig{
			Enabled:    true,
			Path:       cfg.Logging.File.Path,
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

	return zerowrap.New(logConfig), nil, nil
}

// createServices creates all the application services.
func createServices(ctx context.Context, v *viper.Viper, cfg Config, log zerowrap.Logger) (*services, error) {
	svc := &services{}
	var err error

	// Create output adapters
	if svc.runtime, svc.eventBus, err = createOutputAdapters(ctx, log); err != nil {
		return nil, err
	}

	// Create storage
	if svc.blobStorage, svc.manifestStorage, err = createStorage(cfg, log); err != nil {
		return nil, err
	}

	// Create env loader
	if svc.envLoader, err = createEnvLoader(cfg, log); err != nil {
		return nil, err
	}

	// Create log writer
	if svc.logWriter, err = createLogWriter(cfg, log); err != nil {
		return nil, err
	}

	// Create use case services
	svc.configSvc = config.NewService(v, svc.eventBus)
	if err := svc.configSvc.Load(ctx); err != nil {
		return nil, log.WrapErr(err, "failed to load configuration")
	}

	svc.containerSvc = createContainerService(v, cfg, svc)
	svc.registrySvc = registrySvc.NewService(svc.blobStorage, svc.manifestStorage, svc.eventBus)
	svc.proxySvc = proxy.NewService(svc.runtime, svc.containerSvc, svc.configSvc, proxy.Config{
		RegistryDomain: cfg.Server.RegistryDomain,
		RegistryPort:   cfg.Server.RegistryPort,
	})

	return svc, nil
}

// createOutputAdapters creates the Docker runtime and event bus.
func createOutputAdapters(ctx context.Context, log zerowrap.Logger) (*docker.Runtime, *eventbus.InMemory, error) {
	runtime, err := docker.NewRuntime()
	if err != nil {
		return nil, nil, log.WrapErr(err, "failed to create Docker runtime")
	}

	if err := runtime.Ping(ctx); err != nil {
		return nil, nil, log.WrapErr(err, "Docker is not available")
	}

	dockerVersion, _ := runtime.Version(ctx)
	log.Info().Str("docker_version", dockerVersion).Msg("Docker runtime initialized")

	eventBus := eventbus.NewInMemory(100, log)

	return runtime, eventBus, nil
}

// createStorage creates blob and manifest storage.
func createStorage(cfg Config, log zerowrap.Logger) (*filesystem.BlobStorage, *filesystem.ManifestStorage, error) {
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = "/var/lib/gordon"
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
func createEnvLoader(cfg Config, log zerowrap.Logger) (*envloader.FileLoader, error) {
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = "/var/lib/gordon"
	}

	envDir := cfg.Env.Dir
	if envDir == "" {
		envDir = filepath.Join(dataDir, "env")
	}

	envLoader, err := envloader.NewFileLoader(envDir, log)
	if err != nil {
		return nil, log.WrapErr(err, "failed to create env loader")
	}

	// Register secret providers
	passProvider := secrets.NewPassProvider(log)
	if passProvider.IsAvailable() {
		envLoader.RegisterSecretProvider(passProvider)
		log.Debug().Msg("pass secret provider registered")
	}

	sopsProvider := secrets.NewSopsProvider(log)
	if sopsProvider.IsAvailable() {
		envLoader.RegisterSecretProvider(sopsProvider)
		log.Debug().Msg("sops secret provider registered")
	}

	return envLoader, nil
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
			dataDir = "/var/lib/gordon"
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

// createContainerService creates the container service with configuration.
func createContainerService(v *viper.Viper, cfg Config, svc *services) *container.Service {
	containerConfig := container.Config{
		RegistryAuthEnabled: cfg.RegistryAuth.Enabled,
		RegistryDomain:      cfg.Server.RegistryDomain,
		RegistryUsername:    cfg.RegistryAuth.Username,
		RegistryPassword:    cfg.RegistryAuth.Password,
		VolumeAutoCreate:    v.GetBool("volumes.auto_create"),
		VolumePrefix:        v.GetString("volumes.prefix"),
		VolumePreserve:      v.GetBool("volumes.preserve"),
		NetworkIsolation:    v.GetBool("network_isolation.enabled"),
		NetworkPrefix:       v.GetString("network_isolation.network_prefix"),
		DNSSuffix:           v.GetString("network_isolation.dns_suffix"),
		NetworkGroups:       svc.configSvc.GetNetworkGroups(),
		Attachments:         svc.configSvc.GetAttachments(),
	}
	return container.NewService(svc.runtime, svc.envLoader, svc.eventBus, svc.logWriter, containerConfig)
}

// registerEventHandlers registers all event handlers.
func registerEventHandlers(ctx context.Context, svc *services) error {
	imagePushedHandler := container.NewImagePushedHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(imagePushedHandler); err != nil {
		return fmt.Errorf("failed to subscribe image pushed handler: %w", err)
	}

	configReloadHandler := container.NewConfigReloadHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(configReloadHandler); err != nil {
		return fmt.Errorf("failed to subscribe config reload handler: %w", err)
	}

	// Proxy cache invalidation on container deployment (for zero-downtime)
	containerDeployedHandler := proxy.NewContainerDeployedHandler(ctx, svc.proxySvc)
	if err := svc.eventBus.Subscribe(containerDeployedHandler); err != nil {
		return fmt.Errorf("failed to subscribe container deployed handler: %w", err)
	}

	return nil
}

// setupConfigHotReload sets up Viper config hot reload.
func setupConfigHotReload(ctx context.Context, v *viper.Viper, svc *services, log zerowrap.Logger) {
	v.OnConfigChange(func(e fsnotify.Event) {
		log.Info().Str("file", e.Name).Msg("config file changed")

		if err := v.ReadInConfig(); err != nil {
			log.Error().Err(err).Msg("failed to reload config")
			return
		}

		if err := svc.configSvc.Load(ctx); err != nil {
			log.Error().Err(err).Msg("failed to reload configuration")
			return
		}

		svc.proxySvc.UpdateConfig(proxy.Config{
			RegistryDomain: v.GetString("server.registry_domain"),
			RegistryPort:   v.GetInt("server.registry_port"),
		})
	})
	v.WatchConfig()
}

// syncAndAutoStart syncs existing containers and auto-starts if configured.
func syncAndAutoStart(ctx context.Context, svc *services, log zerowrap.Logger) {
	if err := svc.containerSvc.SyncContainers(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to sync existing containers")
	}

	if svc.configSvc.IsAutoRouteEnabled() {
		if err := svc.containerSvc.AutoStart(ctx); err != nil {
			log.Warn().Err(err).Msg("failed to auto-start containers")
		}
	}
}

// createHTTPHandlers creates HTTP handlers with middleware.
func createHTTPHandlers(svc *services, cfg Config, log zerowrap.Logger) (http.Handler, http.Handler) {
	registryHandler := registry.NewHandler(svc.registrySvc, svc.blobStorage, svc.eventBus, log)

	registryMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log),
	}

	if cfg.RegistryAuth.Enabled {
		registryMiddlewares = append(registryMiddlewares,
			middleware.RegistryAuth(cfg.RegistryAuth.Username, cfg.RegistryAuth.Password, log))
	}

	registryWithMiddleware := middleware.Chain(registryMiddlewares...)(registryHandler)

	proxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log),
		middleware.CORS,
	}

	proxyWithMiddleware := middleware.Chain(proxyMiddlewares...)(svc.proxySvc)

	return registryWithMiddleware, proxyWithMiddleware
}

// runServers starts the HTTP servers and waits for shutdown.
func runServers(ctx context.Context, cfg Config, registryHandler, proxyHandler http.Handler, containerSvc *container.Service, log zerowrap.Logger) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	errChan := make(chan error, 2)

	go startServer(fmt.Sprintf(":%d", cfg.Server.RegistryPort), registryHandler, "registry", errChan, log)
	go startServer(fmt.Sprintf(":%d", cfg.Server.Port), proxyHandler, "proxy", errChan, log)

	log.Info().
		Int("proxy_port", cfg.Server.Port).
		Int("registry_port", cfg.Server.RegistryPort).
		Msg("Gordon is running")

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		log.Info().Msg("shutdown signal received")
	}

	log.Info().Msg("shutting down Gordon...")

	if err := containerSvc.Shutdown(ctx); err != nil {
		log.Warn().Err(err).Msg("error during container shutdown")
	}

	log.Info().Msg("Gordon stopped")
	return nil
}

// startServer starts an HTTP server.
func startServer(addr string, handler http.Handler, name string, errChan chan<- error, log zerowrap.Logger) {
	log.Info().Str("address", addr).Msgf("%s server starting", name)

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		errChan <- fmt.Errorf("%s server error: %w", name, err)
	}
}

// SendReloadSignal sends SIGUSR1 to the running Gordon process.
func SendReloadSignal() error {
	pidFile := findPidFile()
	if pidFile == "" {
		return fmt.Errorf("gordon PID file not found, is Gordon running?")
	}

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil {
		return fmt.Errorf("failed to parse PID: %w", err)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}

	if err := process.Signal(syscall.SIGUSR1); err != nil {
		return fmt.Errorf("failed to send reload signal: %w", err)
	}

	return nil
}

// createPidFile creates a PID file for the Gordon process.
func createPidFile(log zerowrap.Logger) string {
	pid := os.Getpid()

	locations := []string{
		"/tmp/gordon.pid",
		filepath.Join(os.TempDir(), "gordon.pid"),
	}

	if homeDir, err := os.UserHomeDir(); err == nil {
		locations = append(locations, filepath.Join(homeDir, ".gordon.pid"))
	}

	for _, location := range locations {
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

// findPidFile finds the Gordon PID file.
func findPidFile() string {
	locations := []string{
		"/tmp/gordon.pid",
		filepath.Join(os.TempDir(), "gordon.pid"),
	}

	if homeDir, err := os.UserHomeDir(); err == nil {
		locations = append(locations, filepath.Join(homeDir, ".gordon.pid"))
	}

	for _, location := range locations {
		if _, err := os.Stat(location); err == nil {
			return location
		}
	}

	return ""
}

// loadConfig loads configuration from file and sets defaults.
func loadConfig(v *viper.Viper, configPath string) error {
	v.SetDefault("server.port", 80)
	v.SetDefault("server.registry_port", 5000)
	v.SetDefault("server.data_dir", "/var/lib/gordon")
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
	v.SetDefault("env.dir", "/var/lib/gordon/env")
	v.SetDefault("registry_auth.enabled", false)

	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("gordon")
		v.SetConfigType("yaml")
		v.AddConfigPath("/etc/gordon")
		v.AddConfigPath("$HOME/.gordon")
		v.AddConfigPath(".")
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return fmt.Errorf("failed to read config file: %w", err)
		}
	}

	v.SetEnvPrefix("GORDON")
	v.AutomaticEnv()

	return nil
}
