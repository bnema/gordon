// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
	"gordon/internal/adapters/out/tokenstore"

	// Adapters - Input
	"gordon/internal/adapters/in/http/middleware"
	"gordon/internal/adapters/in/http/registry"

	// Boundaries
	"gordon/internal/boundaries/out"

	// Domain
	"gordon/internal/domain"

	// Use cases
	"gordon/internal/usecase/auth"
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

	Secrets struct {
		Backend string `mapstructure:"backend"` // "pass", "sops", or "unsafe"
	} `mapstructure:"secrets"`

	RegistryAuth struct {
		Enabled      bool   `mapstructure:"enabled"`
		Type         string `mapstructure:"type"` // "password" or "token"
		Username     string `mapstructure:"username"`
		Password     string `mapstructure:"password"`      // deprecated: use password_hash
		PasswordHash string `mapstructure:"password_hash"` // path in secrets backend
		TokenSecret  string `mapstructure:"token_secret"`  // path in secrets backend
		TokenExpiry  string `mapstructure:"token_expiry"`  // e.g., "720h"
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
	tokenStore      out.TokenStore
	configSvc       *config.Service
	containerSvc    *container.Service
	registrySvc     *registrySvc.Service
	proxySvc        *proxy.Service
	authSvc         *auth.Service
	tokenHandler    *registry.TokenHandler
	internalRegUser string
	internalRegPass string
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
	return runServers(ctx, cfg, registryHandler, proxyHandler, svc.containerSvc, svc.eventBus, log)
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

	// Create auth service (if enabled)
	if svc.tokenStore, svc.authSvc, err = createAuthService(ctx, cfg, log); err != nil {
		return nil, err
	}

	// Generate internal registry credentials for loopback-only pulls
	if cfg.RegistryAuth.Enabled {
		svc.internalRegUser, svc.internalRegPass, err = generateInternalRegistryAuth()
		if err != nil {
			return nil, log.WrapErr(err, "failed to generate internal registry credentials")
		}

		// Persist credentials to file for CLI access (gordon auth internal)
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

	svc.containerSvc = createContainerService(v, cfg, svc)
	svc.registrySvc = registrySvc.NewService(svc.blobStorage, svc.manifestStorage, svc.eventBus)
	svc.proxySvc = proxy.NewService(svc.runtime, svc.containerSvc, svc.configSvc, proxy.Config{
		RegistryDomain: cfg.Server.RegistryDomain,
		RegistryPort:   cfg.Server.RegistryPort,
	})

	// Create token handler for registry token endpoint
	if svc.authSvc != nil {
		internalAuth := registry.InternalAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		svc.tokenHandler = registry.NewTokenHandler(svc.authSvc, internalAuth, log)
	}

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
func createEnvLoader(cfg Config, log zerowrap.Logger) (*envloader.FileLoader, error) {
	dataDir := cfg.Server.DataDir
	if dataDir == "" {
		dataDir = DefaultDataDir()
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

const internalRegistryUsername = "gordon-internal"

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

// getInternalCredentialsFile returns the path to the internal credentials file.
func getInternalCredentialsFile() string {
	return filepath.Join(os.TempDir(), "gordon-internal-creds.json")
}

// persistInternalCredentials saves the internal registry credentials to a temp file.
// Security note: Credentials are stored in the system temp directory with 0600 permissions.
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
	if err := os.WriteFile(credFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write credentials file: %w", err)
	}
	return nil
}

// cleanupInternalCredentials removes the internal credentials file.
func cleanupInternalCredentials() {
	_ = os.Remove(getInternalCredentialsFile())
}

// GetInternalCredentials reads the internal registry credentials from file.
// This is used by the CLI to display credentials for manual recovery.
func GetInternalCredentials() (*InternalCredentials, error) {
	credFile := getInternalCredentialsFile()
	data, err := os.ReadFile(credFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read credentials file (is Gordon running?): %w", err)
	}
	var creds InternalCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}
	return &creds, nil
}

// createAuthService creates the authentication service and token store.
func createAuthService(ctx context.Context, cfg Config, log zerowrap.Logger) (out.TokenStore, *auth.Service, error) {
	if !cfg.RegistryAuth.Enabled {
		return nil, nil, nil
	}

	authType := resolveAuthType(cfg.RegistryAuth.Type)
	backend := resolveSecretsBackend(cfg.Secrets.Backend)
	dataDir := resolveDataDir(cfg.Server.DataDir)

	store, err := createTokenStore(authType, backend, dataDir, log)
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

func resolveAuthType(authType string) domain.AuthType {
	if authType == "token" {
		return domain.AuthTypeToken
	}
	return domain.AuthTypePassword
}

func resolveSecretsBackend(backend string) domain.SecretsBackend {
	switch backend {
	case "pass":
		return domain.SecretsBackendPass
	case "sops":
		return domain.SecretsBackendSops
	case "unsafe", "":
		return domain.SecretsBackendUnsafe
	default:
		return domain.SecretsBackendUnsafe
	}
}

func resolveDataDir(dataDir string) string {
	if dataDir == "" {
		return DefaultDataDir()
	}
	return dataDir
}

func createTokenStore(authType domain.AuthType, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) (out.TokenStore, error) {
	if authType != domain.AuthTypeToken {
		return nil, nil
	}

	store, err := tokenstore.NewStore(backend, dataDir, log)
	if err != nil {
		return nil, log.WrapErr(err, "failed to create token store")
	}
	return store, nil
}

func buildAuthConfig(ctx context.Context, cfg Config, authType domain.AuthType, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) (auth.Config, error) {
	authConfig := auth.Config{
		Enabled:  cfg.RegistryAuth.Enabled,
		AuthType: authType,
		Username: cfg.RegistryAuth.Username,
	}

	switch authType {
	case domain.AuthTypePassword:
		hash, err := loadPasswordHash(ctx, cfg, backend, dataDir, log)
		if err != nil {
			return auth.Config{}, err
		}
		authConfig.PasswordHash = hash
	case domain.AuthTypeToken:
		secret, expiry, err := loadTokenConfig(ctx, cfg, backend, dataDir, log)
		if err != nil {
			return auth.Config{}, err
		}
		authConfig.TokenSecret = secret
		authConfig.TokenExpiry = expiry
	}

	return authConfig, nil
}

func loadPasswordHash(ctx context.Context, cfg Config, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) (string, error) {
	if cfg.RegistryAuth.PasswordHash != "" {
		hash, err := loadSecret(ctx, backend, cfg.RegistryAuth.PasswordHash, dataDir, log)
		if err != nil {
			return "", log.WrapErr(err, "failed to load password hash")
		}
		return hash, nil
	}

	if cfg.RegistryAuth.Password != "" {
		log.Warn().Msg("using plain password in config is deprecated, use password_hash with a secrets backend")
		return cfg.RegistryAuth.Password, nil
	}

	return "", nil
}

func loadTokenConfig(ctx context.Context, cfg Config, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) ([]byte, time.Duration, error) {
	secret, err := loadTokenSecret(ctx, cfg, backend, dataDir, log)
	if err != nil {
		return nil, 0, err
	}

	expiry, err := parseTokenExpiry(cfg.RegistryAuth.TokenExpiry)
	if err != nil {
		return nil, 0, err
	}

	return secret, expiry, nil
}

func loadTokenSecret(ctx context.Context, cfg Config, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) ([]byte, error) {
	if cfg.RegistryAuth.TokenSecret == "" {
		return nil, fmt.Errorf("token_secret is required for token authentication")
	}

	secret, err := loadSecret(ctx, backend, cfg.RegistryAuth.TokenSecret, dataDir, log)
	if err != nil {
		return nil, log.WrapErr(err, "failed to load token secret")
	}

	return []byte(secret), nil
}

func parseTokenExpiry(expiry string) (time.Duration, error) {
	if expiry == "" {
		return 0, nil
	}

	parsed, err := time.ParseDuration(expiry)
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

// createContainerService creates the container service with configuration.
func createContainerService(v *viper.Viper, cfg Config, svc *services) *container.Service {
	containerConfig := container.Config{
		RegistryAuthEnabled:      cfg.RegistryAuth.Enabled,
		RegistryDomain:           cfg.Server.RegistryDomain,
		RegistryPort:             cfg.Server.RegistryPort,
		RegistryUsername:         cfg.RegistryAuth.Username,
		RegistryPassword:         cfg.RegistryAuth.Password,
		InternalRegistryUsername: svc.internalRegUser,
		InternalRegistryPassword: svc.internalRegPass,
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

	manualReloadHandler := container.NewManualReloadHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(manualReloadHandler); err != nil {
		return fmt.Errorf("failed to subscribe manual reload handler: %w", err)
	}

	manualDeployHandler := container.NewManualDeployHandler(ctx, svc.containerSvc, svc.configSvc)
	if err := svc.eventBus.Subscribe(manualDeployHandler); err != nil {
		return fmt.Errorf("failed to subscribe manual deploy handler: %w", err)
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

		// Clear proxy target cache to pick up external route changes
		if err := svc.proxySvc.RefreshTargets(ctx); err != nil {
			log.Warn().Err(err).Msg("failed to refresh proxy targets")
		}

		// Publish config reload event to trigger container updates
		if err := svc.eventBus.Publish(domain.EventConfigReload, nil); err != nil {
			log.Error().Err(err).Msg("failed to publish config reload event")
		}
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
		if err := svc.containerSvc.AutoStart(ctx, routes); err != nil {
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

	if cfg.RegistryAuth.Enabled && svc.authSvc != nil {
		internalAuth := middleware.InternalRegistryAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		registryMiddlewares = append(registryMiddlewares,
			middleware.RegistryAuthV2(svc.authSvc, internalAuth, log))
	}

	registryWithMiddleware := middleware.Chain(registryMiddlewares...)(registryHandler)

	// Create a mux that routes /v2/token to the token handler (no auth required)
	// and all other /v2/* routes to the registry handler (with auth)
	registryMux := http.NewServeMux()
	if svc.tokenHandler != nil {
		// Token endpoint is NOT protected by auth - it's where clients get tokens
		tokenWithLogging := middleware.Chain(
			middleware.PanicRecovery(log),
			middleware.RequestLogger(log),
		)(svc.tokenHandler)
		registryMux.Handle("/v2/token", tokenWithLogging)
	}
	registryMux.Handle("/v2/", registryWithMiddleware)

	proxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log),
		middleware.CORS,
	}

	proxyWithMiddleware := middleware.Chain(proxyMiddlewares...)(svc.proxySvc)

	return registryMux, proxyWithMiddleware
}

// runServers starts the HTTP servers and waits for shutdown.
// Signal handling notes:
// - SIGINT/SIGTERM: Triggers graceful shutdown via signal.NotifyContext
// - SIGUSR1: Triggers config reload without restart
// - SIGUSR2: Triggers manual deploy for a specific route
// The deferred signal.Stop calls ensure signal handlers are properly
// cleaned up before program exit, preventing signal handler leaks.
func runServers(ctx context.Context, cfg Config, registryHandler, proxyHandler http.Handler, containerSvc *container.Service, eventBus out.EventBus, log zerowrap.Logger) error {
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

	errChan := make(chan error, 2)

	go startServer(fmt.Sprintf(":%d", cfg.Server.RegistryPort), registryHandler, "registry", errChan, log)
	go startServer(fmt.Sprintf(":%d", cfg.Server.Port), proxyHandler, "proxy", errChan, log)

	log.Info().
		Int("proxy_port", cfg.Server.Port).
		Int("registry_port", cfg.Server.RegistryPort).
		Msg("Gordon is running")

	for {
		select {
		case err := <-errChan:
			return err
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
			goto shutdown
		}
	}

shutdown:
	log.Info().Msg("shutting down Gordon...")

	if err := containerSvc.Shutdown(ctx); err != nil {
		log.Warn().Err(err).Msg("error during container shutdown")
	}

	// Clean up internal credentials file
	cleanupInternalCredentials()

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
		ReadTimeout:       5 * time.Minute,   // Timeout for reading entire request
		WriteTimeout:      5 * time.Minute,   // Timeout for writing response
		IdleTimeout:       120 * time.Second, // Timeout for idle keep-alive connections
		MaxHeaderBytes:    1 << 20,           // 1MB max header size
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
	pidFile := findPidFile()
	if pidFile == "" {
		_ = os.Remove(deployFile)
		return "", fmt.Errorf("gordon PID file not found, is Gordon running?")
	}

	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		_ = os.Remove(deployFile)
		return "", fmt.Errorf("failed to read PID file: %w", err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil {
		_ = os.Remove(deployFile)
		return "", fmt.Errorf("failed to parse PID: %w", err)
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		_ = os.Remove(deployFile)
		return "", fmt.Errorf("failed to find process: %w", err)
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
	v.SetDefault("server.data_dir", DefaultDataDir())
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
	v.SetDefault("secrets.backend", "unsafe")
	v.SetDefault("registry_auth.enabled", false)
	v.SetDefault("registry_auth.type", "password")
	v.SetDefault("registry_auth.token_expiry", "720h")

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
