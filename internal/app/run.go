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
	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/adapters/out/envloader"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/adapters/out/httpprober"
	"github.com/bnema/gordon/internal/adapters/out/logwriter"
	"github.com/bnema/gordon/internal/adapters/out/ratelimit"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"

	// Adapters - Input
	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	authhandler "github.com/bnema/gordon/internal/adapters/in/http/auth"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/adapters/in/http/registry"

	// Boundaries
	"github.com/bnema/gordon/internal/boundaries/out"

	// Domain
	"github.com/bnema/gordon/internal/domain"

	// Use cases
	"github.com/bnema/gordon/internal/usecase/auth"
	"github.com/bnema/gordon/internal/usecase/config"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/health"
	"github.com/bnema/gordon/internal/usecase/logs"
	"github.com/bnema/gordon/internal/usecase/proxy"
	registrySvc "github.com/bnema/gordon/internal/usecase/registry"
	secretsSvc "github.com/bnema/gordon/internal/usecase/secrets"

	// Pkg
	"github.com/bnema/gordon/pkg/bytesize"
	"github.com/bnema/gordon/pkg/duration"
)

// Config holds the application configuration.
type Config struct {
	Server struct {
		Port             int    `mapstructure:"port"`
		RegistryPort     int    `mapstructure:"registry_port"`
		GordonDomain     string `mapstructure:"gordon_domain"`
		RegistryDomain   string `mapstructure:"registry_domain"` // Deprecated: use gordon_domain
		DataDir          string `mapstructure:"data_dir"`
		MaxProxyBodySize string `mapstructure:"max_proxy_body_size"` // e.g., "512MB", "1GB"
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
		Type           string `mapstructure:"type"`            // "password" or "token"
		SecretsBackend string `mapstructure:"secrets_backend"` // "pass", "sops", or "unsafe"
		Username       string `mapstructure:"username"`
		Password       string `mapstructure:"password"`      // deprecated: use password_hash
		PasswordHash   string `mapstructure:"password_hash"` // path in secrets backend
		TokenSecret    string `mapstructure:"token_secret"`  // path in secrets backend
		TokenExpiry    string `mapstructure:"token_expiry"`  // e.g., "720h", "30d"
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
}

// services holds all the services used by the application.
type services struct {
	runtime         *docker.Runtime
	eventBus        *eventbus.InMemory
	blobStorage     *filesystem.BlobStorage
	manifestStorage *filesystem.ManifestStorage
	envLoader       out.EnvLoader
	logWriter       *logwriter.LogWriter
	tokenStore      out.TokenStore
	configSvc       *config.Service
	containerSvc    *container.Service
	registrySvc     *registrySvc.Service
	proxySvc        *proxy.Service
	authSvc         *auth.Service
	authHandler     *authhandler.Handler
	adminHandler    *admin.Handler
	internalRegUser string
	internalRegPass string
	envDir          string
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
	if err := registerEventHandlers(ctx, svc, cfg); err != nil {
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
	if svc.runtime, svc.eventBus, err = createOutputAdapters(ctx, log); err != nil {
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

	if err := setupInternalRegistryAuth(cfg, svc, log); err != nil {
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

	secretSvc := secretsSvc.NewService(domainSecretStore, log)

	if svc.containerSvc, err = createContainerService(ctx, v, cfg, svc, log); err != nil {
		return nil, err
	}
	svc.registrySvc = registrySvc.NewService(svc.blobStorage, svc.manifestStorage, svc.eventBus)

	// Parse max_proxy_body_size config (default: 512MB)
	maxProxyBodySize := int64(512 << 20) // 512MB default
	if cfg.Server.MaxProxyBodySize != "" {
		parsedSize, err := bytesize.Parse(cfg.Server.MaxProxyBodySize)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "invalid server.max_proxy_body_size configuration", map[string]any{"value": cfg.Server.MaxProxyBodySize})
		}
		maxProxyBodySize = parsedSize
	}

	svc.proxySvc = proxy.NewService(svc.runtime, svc.containerSvc, svc.configSvc, proxy.Config{
		RegistryDomain: cfg.Server.RegistryDomain,
		RegistryPort:   cfg.Server.RegistryPort,
		MaxBodySize:    maxProxyBodySize,
	})

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
	healthSvc := health.NewService(svc.configSvc, svc.containerSvc, prober, log)

	// Create log service for accessing logs via admin API
	logSvc := logs.NewService(resolveLogFilePath(cfg), svc.containerSvc, svc.runtime, log)

	// Create admin handler for admin API
	svc.adminHandler = admin.NewHandler(svc.configSvc, svc.authSvc, svc.containerSvc, healthSvc, secretSvc, logSvc, svc.registrySvc, svc.eventBus, log)

	return svc, nil
}

func setupInternalRegistryAuth(cfg Config, svc *services, log zerowrap.Logger) error {
	if !cfg.Auth.Enabled {
		return nil
	}

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
	backend := resolveSecretsBackend(cfg.Auth.SecretsBackend)

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
	return filepath.Join(cfg.Server.DataDir, "logs", "gordon.log")
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
	if !cfg.Auth.Enabled {
		return nil, nil, nil
	}

	authType := resolveAuthType(cfg)
	backend := resolveSecretsBackend(cfg.Auth.SecretsBackend)
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
// If password_hash is configured, password auth is available (plus tokens).
// If only token_secret is configured, token-only mode.
// The explicit "type" field is deprecated but still respected for backwards compat.
func resolveAuthType(cfg Config) domain.AuthType {
	// Explicit type takes precedence (backwards compatibility)
	if cfg.Auth.Type == "token" {
		return domain.AuthTypeToken
	}
	if cfg.Auth.Type == "password" {
		return domain.AuthTypePassword
	}

	// Infer from config: password auth if password_hash is configured
	if cfg.Auth.PasswordHash != "" || cfg.Auth.Password != "" {
		return domain.AuthTypePassword
	}

	// Default to token-only
	return domain.AuthTypeToken
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

	// Password config only needed for password auth mode
	if authType == domain.AuthTypePassword {
		hash, err := loadPasswordHash(ctx, cfg, backend, dataDir, log)
		if err != nil {
			return auth.Config{}, err
		}
		if hash == "" {
			return auth.Config{}, errAuthNotConfigured()
		}
		authConfig.PasswordHash = hash
	}

	return authConfig, nil
}

// errAuthNotConfigured returns an error when auth is enabled but credentials are not configured.
func errAuthNotConfigured() error {
	return fmt.Errorf("auth is enabled by default, configure auth type and secrets backend. See: https://gordon.bnema.dev/docs/config/auth")
}

func loadPasswordHash(ctx context.Context, cfg Config, backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) (string, error) {
	if cfg.Auth.PasswordHash != "" {
		hash, err := loadSecret(ctx, backend, cfg.Auth.PasswordHash, dataDir, log)
		if err != nil {
			return "", log.WrapErr(err, "failed to load password hash")
		}
		return hash, nil
	}

	if cfg.Auth.Password != "" {
		log.Warn().Msg("using plain password in config is deprecated, use password_hash with a secrets backend")
		return cfg.Auth.Password, nil
	}

	return "", nil
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

	// Check environment variable first
	if envSecret := os.Getenv(TokenSecretEnvVar); envSecret != "" {
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

// createContainerService creates the container service with configuration.
func createContainerService(ctx context.Context, v *viper.Viper, cfg Config, svc *services, log zerowrap.Logger) (*container.Service, error) {
	containerConfig := container.Config{
		RegistryAuthEnabled:      cfg.Auth.Enabled,
		RegistryDomain:           cfg.Server.RegistryDomain,
		RegistryPort:             cfg.Server.RegistryPort,
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

	if cfg.Auth.Enabled && svc.authSvc != nil {
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
	}

	return container.NewService(svc.runtime, svc.envLoader, svc.eventBus, svc.logWriter, containerConfig), nil
}

// registerEventHandlers registers all event handlers.
func registerEventHandlers(ctx context.Context, svc *services, cfg Config) error {
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

		// Update proxy config from viper (reads directly from viper, not memory)
		svc.proxySvc.UpdateConfig(proxy.Config{
			RegistryDomain: v.GetString("server.registry_domain"),
			RegistryPort:   v.GetInt("server.registry_port"),
		})

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
		if err := svc.containerSvc.AutoStart(ctx, routes); err != nil {
			log.Warn().Err(err).Msg("failed to auto-start containers")
		}
	}
}

// createHTTPHandlers creates HTTP handlers with middleware.
func createHTTPHandlers(svc *services, cfg Config, log zerowrap.Logger) (http.Handler, http.Handler) {
	// Parse trusted proxies once for all middleware chains.
	// This ensures consistent IP extraction across logging, rate limiting, and auth.
	trustedNets := middleware.ParseTrustedProxies(cfg.API.RateLimit.TrustedProxies)

	registryHandler := registry.NewHandler(svc.registrySvc, log)

	registryMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
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

	if cfg.Auth.Enabled && svc.authSvc != nil {
		internalAuth := middleware.InternalRegistryAuth{
			Username: svc.internalRegUser,
			Password: svc.internalRegPass,
		}
		registryMiddlewares = append(registryMiddlewares,
			middleware.RegistryAuthV2(svc.authSvc, internalAuth, log))
	}

	registryWithMiddleware := middleware.Chain(registryMiddlewares...)(registryHandler)

	// Create a mux that routes:
	// - /auth/* to auth handler (no auth required, for password login and token exchange)
	// - /v2/* to registry handler (with auth)
	// - /admin/* to admin handler (with auth)
	registryMux := http.NewServeMux()
	if svc.authHandler != nil {
		// Auth endpoints are NOT protected by auth - they're where clients authenticate
		// but still need rate limiting to prevent brute force attacks
		authWithMiddleware := middleware.Chain(
			middleware.PanicRecovery(log),
			middleware.RequestLogger(log, trustedNets),
			middleware.SecurityHeaders,
			rateLimitMiddleware,
		)(svc.authHandler)
		registryMux.Handle("/auth/", authWithMiddleware)
	}
	registryMux.Handle("/v2/", registryWithMiddleware)

	// Add admin API handler with auth middleware
	if svc.adminHandler != nil {
		adminMiddlewares := []func(http.Handler) http.Handler{
			middleware.PanicRecovery(log),
			middleware.RequestLogger(log, trustedNets),
			middleware.SecurityHeaders,
		}
		// Add admin auth middleware if auth is enabled
		if svc.authSvc != nil {
			// Create rate limiters for admin API - uses same config as registry
			var globalLimiter, ipLimiter out.RateLimiter
			if cfg.API.RateLimit.Enabled {
				globalLimiter = ratelimit.NewMemoryStore(cfg.API.RateLimit.GlobalRPS, cfg.API.RateLimit.Burst, log)
				ipLimiter = ratelimit.NewMemoryStore(cfg.API.RateLimit.PerIPRPS, cfg.API.RateLimit.Burst, log)
			}
			adminMiddlewares = append(adminMiddlewares, admin.AuthMiddleware(svc.authSvc, globalLimiter, ipLimiter, trustedNets, log))
		}
		adminWithMiddleware := middleware.Chain(adminMiddlewares...)(svc.adminHandler)
		registryMux.Handle("/admin/", adminWithMiddleware)
	}

	// SECURITY: No CORS middleware on the proxy chain. Backend applications
	// should control their own CORS policies. A blanket Access-Control-Allow-Origin: *
	// would override backend CORS settings and allow any website to make
	// cross-origin authenticated requests to proxied applications.
	proxyMiddlewares := []func(http.Handler) http.Handler{
		middleware.PanicRecovery(log),
		middleware.RequestLogger(log, trustedNets),
		middleware.SecurityHeaders,
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

// findPidFile finds the Gordon PID file.
// Checks secure locations first, then falls back to legacy /tmp location.
func findPidFile() string {
	var locations []string

	// Check secure runtime directory first
	if runtimeDir, err := getSecureRuntimeDir(); err == nil {
		locations = append(locations, filepath.Join(runtimeDir, "gordon.pid"))
	}

	// Check home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		locations = append(locations, filepath.Join(homeDir, ".gordon", "gordon.pid"))
		// Legacy location for backward compatibility
		locations = append(locations, filepath.Join(homeDir, ".gordon.pid"))
	}

	// Legacy /tmp locations for backward compatibility
	locations = append(locations, filepath.Join(os.TempDir(), "gordon.pid"))
	locations = append(locations, "/tmp/gordon.pid")

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
	v.SetDefault("auth.enabled", true)
	// Note: auth.type is intentionally not set - it's inferred from config
	// If password_hash is set -> password mode, otherwise -> token mode
	v.SetDefault("auth.secrets_backend", "unsafe")
	v.SetDefault("auth.token_expiry", "720h")
	v.SetDefault("api.rate_limit.enabled", true)
	v.SetDefault("api.rate_limit.global_rps", 500)
	v.SetDefault("api.rate_limit.per_ip_rps", 50)
	v.SetDefault("api.rate_limit.burst", 100)
	v.SetDefault("auto_route.enabled", false)
	v.SetDefault("network_isolation.enabled", false)
	v.SetDefault("network_isolation.network_prefix", "gordon")
	v.SetDefault("network_isolation.dns_suffix", ".internal")
	v.SetDefault("volumes.auto_create", true)
	v.SetDefault("volumes.prefix", "gordon")
	v.SetDefault("volumes.preserve", true)
	v.SetDefault("deploy.pull_policy", container.PullPolicyIfTagChanged)

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
