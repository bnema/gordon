// Package app provides the application initialization and wiring.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	// Adapters - Output
	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/envloader"
	"github.com/bnema/gordon/internal/adapters/out/eventbus"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/adapters/out/logwriter"
	"github.com/bnema/gordon/internal/adapters/out/secrets"
	"github.com/bnema/gordon/internal/adapters/out/tokenstore"

	// Boundaries
	"github.com/bnema/gordon/internal/boundaries/out"

	// Domain
	"github.com/bnema/gordon/internal/domain"

	// Use cases
	"github.com/bnema/gordon/internal/usecase/auth"
	"github.com/bnema/gordon/internal/usecase/container"

	// Pkg
	"github.com/bnema/gordon/pkg/duration"
)

// Config holds the application configuration.
type Config struct {
	Server struct {
		Port           int    `mapstructure:"port"`
		RegistryPort   int    `mapstructure:"registry_port"`
		GordonDomain   string `mapstructure:"gordon_domain"`
		RegistryDomain string `mapstructure:"registry_domain"` // Deprecated: use gordon_domain
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

	if cfg.Logging.File.Enabled {
		logPath := cfg.Logging.File.Path
		if logPath == "" {
			// Default to {data_dir}/logs/gordon.log
			logPath = filepath.Join(cfg.Server.DataDir, "logs", "gordon.log")
		}

		log, cleanup, err := zerowrap.NewWithFile(logConfig, zerowrap.FileConfig{
			Enabled:    true,
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

	return zerowrap.New(logConfig), nil, nil
}

// resolveLogFilePath returns the configured log file path or a default.
func resolveLogFilePath(cfg Config) string {
	if cfg.Logging.File.Path != "" {
		return cfg.Logging.File.Path
	}
	if cfg.Logging.File.Enabled {
		return filepath.Join(cfg.Server.DataDir, "logs", "gordon.log")
	}
	return ""
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
