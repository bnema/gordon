package common

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/logger"
	"github.com/bnema/gordon/pkg/parser"
)

var (
	globalConfig      *Config
	globalConfigOnce  sync.Once
	globalConfigMu    sync.RWMutex
	configLogsPrinted bool
)

// Initialize package-level logging configuration
func init() {
	// Configure the logger from environment variables
	logger.GetLogger().ConfigureFromEnv()
}

type Config struct {
	General         GeneralConfig         `yaml:"General"`
	Http            HttpConfig            `yaml:"Http"`
	Admin           AdminConfig           `yaml:"Admin"`
	ContainerEngine ContainerEngineConfig `yaml:"ContainerEngine"`
	ReverseProxy    ReverseProxyConfig    `yaml:"ReverseProxy"`
	Build           BuildConfig           `yaml:"-"`
}

type BuildConfig struct {
	RunEnv       string `yaml:"-"` // come from env
	BuildVersion string `yaml:"-"` // come from build ldflags
	BuildCommit  string `yaml:"-"` // come from build ldflags
	BuildDate    string `yaml:"-"` // come from build ldflags
	ProxyURL     string `yaml:"-"`
}

type AdminConfig struct {
	Path string `yaml:"path"`
}

type GeneralConfig struct {
	StorageDir        string `yaml:"storageDir"`
	Token             string `yaml:"token"`
	LogLevel          string `yaml:"logLevel"`
	GordonContainerID string `yaml:"gordonContainerID"`
}

type HttpConfig struct {
	Port       string `yaml:"port"`
	Domain     string `yaml:"domain"`
	SubDomain  string `yaml:"subDomain"`
	BackendURL string `yaml:"backendURL"`
	Https      bool   `yaml:"https"`
}

type ContainerEngineConfig struct {
	Sock       string `yaml:"dockersock"`
	PodmanSock string `yaml:"podmansock"`
	Podman     bool   `yaml:"podman"`
	Network    string `yaml:"network"`
}

type ReverseProxyConfig struct {
	Port            string `yaml:"port"`            // Port for the reverse proxy to listen on
	HttpPort        string `yaml:"httpPort"`        // HTTP port (usually 80) for redirecting to HTTPS
	CertDir         string `yaml:"certDir"`         // Directory to store Let's Encrypt certificates
	AutoRenew       bool   `yaml:"autoRenew"`       // Whether to automatically renew certificates
	RenewBefore     int    `yaml:"renewBefore"`     // Days before expiry to renew certificates
	LetsEncryptMode string `yaml:"letsEncryptMode"` // "staging" or "production"
	Email           string `yaml:"email"`           // Email for Let's Encrypt
	CacheSize       int    `yaml:"cacheSize"`       // Size of the certificate cache
	GracePeriod     int    `yaml:"gracePeriod"`     // Shutdown grace period in seconds
	EnableLogs      bool   `yaml:"enableLogs"`      // Whether to enable HTTP request logging (default: true)
}

// Default values
var (
	sock             = "/var/run/docker.sock"
	podmansock       = "/run/podman/podman.sock"
	reverseProxyPort = "443" // Changed to 443 for standard HTTPS
	httpPort         = "80"  // Standard HTTP port
	certDir          = "/certs"
	letsEncryptMode  = "staging"
	autoRenew        = true
	renewBefore      = 30     // days
	cacheSize        = 1000   // entries
	gracePeriod      = 30     // seconds
	defaultLogLevel  = "info" // Default log level
)

// applyDefaultsToConfig applies default values to any fields that have zero values
// Returns true if any defaults were applied
func applyDefaultsToConfig(config *Config) bool {
	defaultsApplied := false

	// Apply defaults to GeneralConfig
	if config.General.LogLevel == "" {
		config.General.LogLevel = defaultLogLevel
		logger.Debug("Applied default value for General.LogLevel", "value", defaultLogLevel)
		defaultsApplied = true
	}

	// Apply defaults to ReverseProxy config
	if config.ReverseProxy.Port == "" {
		config.ReverseProxy.Port = reverseProxyPort
		logger.Debug("Applied default value for ReverseProxy.Port", "value", reverseProxyPort)
		defaultsApplied = true
	}
	if config.ReverseProxy.HttpPort == "" {
		config.ReverseProxy.HttpPort = httpPort
		logger.Debug("Applied default value for ReverseProxy.HttpPort", "value", httpPort)
		defaultsApplied = true
	}
	if config.ReverseProxy.CertDir == "" {
		config.ReverseProxy.CertDir = certDir
		logger.Debug("Applied default value for ReverseProxy.CertDir", "value", certDir)
		defaultsApplied = true
	}
	if config.ReverseProxy.RenewBefore == 0 {
		config.ReverseProxy.RenewBefore = renewBefore
		logger.Debug("Applied default value for ReverseProxy.RenewBefore", "value", renewBefore)
		defaultsApplied = true
	}
	if config.ReverseProxy.LetsEncryptMode == "" {
		config.ReverseProxy.LetsEncryptMode = letsEncryptMode
		logger.Debug("Applied default value for ReverseProxy.LetsEncryptMode", "value", letsEncryptMode)
		defaultsApplied = true
	}
	if config.ReverseProxy.CacheSize == 0 {
		config.ReverseProxy.CacheSize = cacheSize
		logger.Debug("Applied default value for ReverseProxy.CacheSize", "value", cacheSize)
		defaultsApplied = true
	}
	if config.ReverseProxy.GracePeriod == 0 {
		config.ReverseProxy.GracePeriod = gracePeriod
		logger.Debug("Applied default value for ReverseProxy.GracePeriod", "value", gracePeriod)
		defaultsApplied = true
	}
	// Set EnableLogs to true by default if not specified
	if !config.ReverseProxy.EnableLogs {
		config.ReverseProxy.EnableLogs = true
		logger.Debug("Applied default value for ReverseProxy.EnableLogs", "value", true)
		defaultsApplied = true
	}

	// Handle autoRenew
	configFilePath := filepath.Join(getConfigDirMustExist(), "config.yml")
	if fileExists(configFilePath) {
		yamlContent, err := os.ReadFile(configFilePath)
		if err == nil {
			// Check if "autoRenew" is explicitly mentioned in the config file
			if !strings.Contains(string(yamlContent), "autoRenew:") {
				config.ReverseProxy.AutoRenew = autoRenew
				logger.Debug("Applied default value for ReverseProxy.AutoRenew", "value", autoRenew)
				defaultsApplied = true
			} else {
				logger.Debug("Keeping explicit value for ReverseProxy.AutoRenew", "value", config.ReverseProxy.AutoRenew)
			}
		} else {
			// If we can't read the file for some reason, apply the default
			if !config.ReverseProxy.AutoRenew {
				config.ReverseProxy.AutoRenew = autoRenew
				logger.Debug("Applied default value for ReverseProxy.AutoRenew", "value", autoRenew)
				defaultsApplied = true
			}
		}
	} else {
		// If config file doesn't exist yet, set the default
		config.ReverseProxy.AutoRenew = autoRenew
		logger.Debug("Applied default value for ReverseProxy.AutoRenew", "value", autoRenew)
		defaultsApplied = true
	}

	return defaultsApplied
}

// getConfigDirMustExist returns the config directory and creates it if it doesn't exist
// This is a helper function to avoid error handling in applyDefaultsToConfig
func getConfigDirMustExist() string {
	configDir, err := getConfigDir()
	if err != nil {
		// Fallback to a reasonable default if there's an error
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "/tmp/.gordon"
		}
		return filepath.Join(homeDir, ".gordon")
	}
	return configDir
}

// GetGlobalConfig returns the singleton config instance, loading it if necessary
func GetGlobalConfig(buildConfig *BuildConfig) (*Config, error) {
	logger.Debug("GetGlobalConfig: Starting with buildConfig", "buildConfig", buildConfig != nil)

	// Try read lock first
	logger.Debug("GetGlobalConfig: Acquiring read lock")
	globalConfigMu.RLock()
	if globalConfig != nil {
		// If buildConfig is provided, update the Build field
		if buildConfig != nil {
			globalConfig.Build = *buildConfig
		}
		config := globalConfig
		logger.Debug("GetGlobalConfig: Config exists, releasing read lock")
		globalConfigMu.RUnlock()
		logger.Debug("GetGlobalConfig: Using existing global configuration (already loaded)")
		return config, nil
	}
	logger.Debug("GetGlobalConfig: Config not found, releasing read lock")
	globalConfigMu.RUnlock()

	// Need to load the config - acquire write lock
	logger.Debug("GetGlobalConfig: No existing config, acquiring write lock")
	globalConfigMu.Lock()
	defer func() {
		logger.Debug("GetGlobalConfig: Releasing write lock via defer")
		globalConfigMu.Unlock()
	}()

	// Double-check in case another goroutine loaded the config while we were waiting
	if globalConfig != nil {
		// If buildConfig is provided, update the Build field
		if buildConfig != nil {
			globalConfig.Build = *buildConfig
		}
		logger.Debug("GetGlobalConfig: Using existing global configuration (loaded by another goroutine)")
		return globalConfig, nil
	}

	// Initialize config with build info
	config := &Config{}
	if buildConfig != nil {
		config.Build = *buildConfig
	}

	// Load the config once
	var err error
	logger.Debug("GetGlobalConfig: About to execute globalConfigOnce.Do")
	globalConfigOnce.Do(func() {
		logger.Debug("Inside globalConfigOnce.Do, loading configuration...")
		var loadedConfig *Config
		loadedConfig, err = config.LoadConfig()
		if err == nil {
			logger.Debug("GetGlobalConfig: LoadConfig successful, setting globalConfig")
			globalConfig = loadedConfig
			logger.Info("Global configuration loaded successfully")
		} else {
			logger.Error("Error loading global configuration", "error", err)
		}
	})
	logger.Debug("GetGlobalConfig: Completed globalConfigOnce.Do")

	// Return the global config if it was set, otherwise return the local config
	if globalConfig != nil {
		return globalConfig, err
	}

	return config, err
}

func getConfigDir() (string, error) {
	logger.Debug("getConfigDir: Starting getConfigDir function")
	var configDir string

	logger.Debug("getConfigDir: Checking if running in WSL")
	if isWSL() {
		logger.Debug("getConfigDir: Running in WSL")
		// Use XDG_CONFIG_HOME for WSL
		if xdgConfigHome := os.Getenv("XDG_CONFIG_HOME"); xdgConfigHome != "" {
			logger.Debug("getConfigDir: Using XDG_CONFIG_HOME environment variable", "value", xdgConfigHome)
			configDir = filepath.Join(xdgConfigHome, "Gordon")
		} else {
			// If XDG_CONFIG_HOME is not set, fall back to default locations
			logger.Debug("getConfigDir: XDG_CONFIG_HOME not set, getting user home directory")
			homeDir, err := os.UserHomeDir()
			if err != nil {
				// If we can't get the home directory, use a fallback
				logger.Debug("getConfigDir: Failed to get user home directory, using temp dir", "error", err)
				homeDir = os.TempDir() // Use the system's temp directory as a fallback
				logger.Warn("Warning: Unable to determine home directory. Using temp directory", "dir", homeDir)
			}

			configDir = filepath.Join(homeDir, ".config", "Gordon")
			logger.Debug("getConfigDir: Set config directory based on home dir", "dir", configDir)
		}

		logger.Debug("getConfigDir: Returning WSL config directory", "dir", configDir)
		return configDir, nil
	}

	logger.Debug("getConfigDir: Checking if running in container")
	if docker.IsRunningInContainer() {
		logger.Debug("getConfigDir: Running in container, using current directory")
		return ".", nil
	}

	// Check for XDG_CONFIG_HOME first
	logger.Debug("getConfigDir: Checking XDG_CONFIG_HOME environment variable")
	if xdgConfigHome := os.Getenv("XDG_CONFIG_HOME"); xdgConfigHome != "" {
		logger.Debug("getConfigDir: Using XDG_CONFIG_HOME environment variable", "value", xdgConfigHome)
		configDir = filepath.Join(xdgConfigHome, "Gordon")
	} else {
		// If XDG_CONFIG_HOME is not set, fall back to default locations
		logger.Debug("getConfigDir: XDG_CONFIG_HOME not set, getting user home directory")
		homeDir, err := os.UserHomeDir()
		if err != nil {
			logger.Debug("getConfigDir: Failed to get user home directory", "error", err)
			return "", fmt.Errorf("could not determine user home directory: %w", err)
		}

		// For Windows, use the standard application data directory
		if runtime.GOOS == "windows" {
			logger.Debug("getConfigDir: On Windows, using AppData")
			configDir = filepath.Join(homeDir, "AppData", "Local", "Gordon")
		} else {
			// For Unix systems, use the XDG Base Directory Specification
			logger.Debug("getConfigDir: On Unix system, using XDG pattern")
			configDir = filepath.Join(homeDir, ".config", "Gordon")
		}
	}

	logger.Debug("getConfigDir: Returning config directory", "dir", configDir)
	return configDir, nil
}

func readAndUnmarshalConfig(fs fs.FS, filePath string, config *Config) error {
	logger.Debug("readAndUnmarshalConfig: Starting to read and unmarshal config file", "path", filePath)
	err := parser.ParseYAMLFile(fs, filePath, config)
	if err != nil {
		logger.Debug("readAndUnmarshalConfig: Failed to parse YAML file", "error", err)
		return fmt.Errorf("error reading and unmarshaling configuration file: %w", err)
	}
	logger.Debug("readAndUnmarshalConfig: Successfully parsed YAML file")
	return nil
}

// loadConfigFromEnv loads configuration from environment variables
func loadConfigFromEnv(config *Config, printLogs bool) {
	// General Configuration
	if val := os.Getenv("GORDON_STORAGE_DIR"); val != "" {
		config.General.StorageDir = val
		if printLogs {
			logger.Info("Using environment variable GORDON_STORAGE_DIR", "value", val)
		}
	}
	if val := os.Getenv("GORDON_TOKEN"); val != "" {
		config.General.Token = val
		if printLogs {
			logger.Info("Using environment variable GORDON_TOKEN")
		}
	}
	if val := os.Getenv("GORDON_LOG_LEVEL"); val != "" {
		config.General.LogLevel = val
		if printLogs {
			logger.Info("Using environment variable GORDON_LOG_LEVEL", "value", val)
		}
	}

	// HTTP Configuration
	if val := os.Getenv("GORDON_HTTP_PORT"); val != "" {
		config.Http.Port = val
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_PORT", "value", val)
		}
	}
	if val := os.Getenv("GORDON_HTTP_DOMAIN"); val != "" {
		config.Http.Domain = val
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_DOMAIN", "value", val)
		}
	}
	if val := os.Getenv("GORDON_HTTP_SUBDOMAIN"); val != "" {
		config.Http.SubDomain = val
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_SUBDOMAIN", "value", val)
		}
	}
	if val := os.Getenv("GORDON_HTTP_BACKEND_URL"); val != "" {
		config.Http.BackendURL = val
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_BACKEND_URL", "value", val)
		}
	}
	if val := os.Getenv("GORDON_HTTP_HTTPS"); val != "" {
		config.Http.Https = strings.ToLower(val) == "true"
		if printLogs {
			logger.Info("Using environment variable GORDON_HTTP_HTTPS", "value", config.Http.Https)
		}
	}

	// Admin Configuration
	if val := os.Getenv("GORDON_ADMIN_PATH"); val != "" {
		config.Admin.Path = val
		if printLogs {
			logger.Info("Using environment variable GORDON_ADMIN_PATH", "value", val)
		}
	}

	// Container Engine Configuration
	if val := os.Getenv("GORDON_CONTAINER_SOCK"); val != "" {
		config.ContainerEngine.Sock = val
		if printLogs {
			logger.Info("Using environment variable GORDON_CONTAINER_SOCK", "value", val)
		}
	}
	if val := os.Getenv("GORDON_CONTAINER_PODMANSOCK"); val != "" {
		config.ContainerEngine.PodmanSock = val
		if printLogs {
			logger.Info("Using environment variable GORDON_CONTAINER_PODMANSOCK", "value", val)
		}
	}
	if val := os.Getenv("GORDON_CONTAINER_PODMAN"); val != "" {
		config.ContainerEngine.Podman = strings.ToLower(val) == "true"
		if printLogs {
			logger.Info("Using environment variable GORDON_CONTAINER_PODMAN", "value", config.ContainerEngine.Podman)
		}
	} else {
		// Auto-detect Podman if not specified in environment
		isPodman, podmanSocket := docker.DetectPodman()
		if isPodman {
			config.ContainerEngine.Podman = true
			config.ContainerEngine.PodmanSock = podmanSocket
			if printLogs {
				logger.Info("Automatically detected Podman installation",
					"using_podman", true,
					"socket", podmanSocket)
			}
		}
	}
	if val := os.Getenv("GORDON_CONTAINER_NETWORK"); val != "" {
		config.ContainerEngine.Network = val
		if printLogs {
			logger.Info("Using environment variable GORDON_CONTAINER_NETWORK", "value", val)
		}
	}

	// Use Podman socket if Podman is enabled and no Docker socket is specified
	if config.ContainerEngine.Podman && config.ContainerEngine.Sock == "" && config.ContainerEngine.PodmanSock != "" {
		config.ContainerEngine.Sock = config.ContainerEngine.PodmanSock
		if printLogs {
			logger.Info("Setting ContainerEngine.Sock to PodmanSock value", "value", config.ContainerEngine.Sock)
		}
	}

	// Reverse Proxy Configuration
	if val := os.Getenv("GORDON_PROXY_PORT"); val != "" {
		config.ReverseProxy.Port = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_PORT", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_HTTP_PORT"); val != "" {
		config.ReverseProxy.HttpPort = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_HTTP_PORT", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_CERT_DIR"); val != "" {
		config.ReverseProxy.CertDir = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_CERT_DIR", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_AUTO_RENEW"); val != "" {
		config.ReverseProxy.AutoRenew = strings.ToLower(val) == "true"
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_AUTO_RENEW", "value", config.ReverseProxy.AutoRenew)
		}
	}
	if val := os.Getenv("GORDON_PROXY_RENEW_BEFORE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.ReverseProxy.RenewBefore = i
			if printLogs {
				logger.Info("Using environment variable GORDON_PROXY_RENEW_BEFORE", "value", i)
			}
		}
	}
	if val := os.Getenv("GORDON_PROXY_LETSENCRYPT_MODE"); val != "" {
		config.ReverseProxy.LetsEncryptMode = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_LETSENCRYPT_MODE", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_EMAIL"); val != "" {
		config.ReverseProxy.Email = val
		if printLogs {
			logger.Info("Using environment variable GORDON_PROXY_EMAIL", "value", val)
		}
	}
	if val := os.Getenv("GORDON_PROXY_CACHE_SIZE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.ReverseProxy.CacheSize = i
			if printLogs {
				logger.Info("Using environment variable GORDON_PROXY_CACHE_SIZE", "value", i)
			}
		}
	}
	if val := os.Getenv("GORDON_PROXY_GRACE_PERIOD"); val != "" {
		i, err := strconv.Atoi(val)
		if err == nil {
			config.ReverseProxy.GracePeriod = i
		}
		logger.Info("Using environment variable GORDON_PROXY_GRACE_PERIOD", "value", i)
	}

	// Handle GORDON_PROXY_ENABLE_LOGS environment variable
	if val := os.Getenv("GORDON_PROXY_ENABLE_LOGS"); val != "" {
		enableLogs, err := strconv.ParseBool(val)
		if err == nil {
			config.ReverseProxy.EnableLogs = enableLogs
		}
		logger.Info("Using environment variable GORDON_PROXY_ENABLE_LOGS", "value", config.ReverseProxy.EnableLogs)
	}
}

func (config *Config) LoadConfig() (*Config, error) {
	// If we're in a non-interactive environment, use simplified config
	nonInteractiveEnv := os.Getenv("GORDON_NONINTERACTIVE")
	isNonInteractive := nonInteractiveEnv == "true" || nonInteractiveEnv == "1" || nonInteractiveEnv == "yes"

	if isNonInteractive {
		logger.Info("Non-interactive mode detected, using simplified config without prompts")
		// Create base configuration without prompts
		homeDir, err := os.UserHomeDir()
		if err != nil {
			homeDir = "/tmp"
			logger.Warn("Could not get user home directory, using /tmp instead")
		}

		// Get default socket paths
		sock := "/var/run/docker.sock"
		podmansock := "/run/podman/podman.sock"

		// Auto-detect Podman
		isPodman, podmanSocket := docker.DetectPodman()
		if isPodman {
			podmansock = podmanSocket
		}

		*config = Config{
			General: GeneralConfig{
				StorageDir: homeDir + "/.gordon",
				LogLevel:   defaultLogLevel,
			},
			Http: HttpConfig{
				Port:   "8080",
				Domain: "localhost",
				Https:  false,
			},
			Admin: AdminConfig{
				Path: "/admin",
			},
			ContainerEngine: ContainerEngineConfig{
				Sock:       sock,
				PodmanSock: podmansock,
				Podman:     isPodman, // Use the auto-detected value
				Network:    "gordon",
			},
			ReverseProxy: ReverseProxyConfig{
				Port:            reverseProxyPort,
				HttpPort:        httpPort,
				CertDir:         certDir,
				AutoRenew:       autoRenew,
				RenewBefore:     renewBefore,
				LetsEncryptMode: letsEncryptMode,
				CacheSize:       cacheSize,
				GracePeriod:     gracePeriod,
				EnableLogs:      true,
			},
		}

		// If in container, apply env vars
		if docker.IsRunningInContainer() {
			logger.Info("Running in container, overriding configuration with environment variables...")

			loadConfigFromEnv(config, true)
		}

		return config, nil
	}

	// Original LoadConfig implementation continues below for interactive mode
	logger.Debug("LoadConfig: Starting LoadConfig function")

	// Log current execution environment for debugging
	isInContainer := docker.IsRunningInContainer()
	cwd, _ := os.Getwd()
	logger.Debug("LoadConfig: Environment information",
		"isContainer", isInContainer,
		"currentDir", cwd,
		"ENV", os.Getenv("ENV"),
		"GORDON_NONINTERACTIVE", os.Getenv("GORDON_NONINTERACTIVE"))

	logger.Info("Loading config...")

	// Enable extra debugging for this function if requested
	debugLevel := os.Getenv("GORDON_CONFIG_LOG_LEVEL")
	if debugLevel == "trace" {
		logger.Debug("LoadConfig: TRACE mode enabled, will log detailed steps")
	}

	// Get the config directory
	logger.Debug("LoadConfig: About to call getConfigDir()")
	configDir, err := getConfigDir()
	if err != nil {
		logger.Error("LoadConfig: Failed to get config directory", "error", err)
		return nil, fmt.Errorf("error getting configuration directory: %w", err)
	}

	logger.Debug("LoadConfig: Got config directory", "dir", configDir)
	logger.Info("Using configuration directory", "dir", configDir)

	// Check for directly mounted config.yml in root when in container
	var configFilePath string
	logger.Debug("LoadConfig: Checking if running in container")
	if docker.IsRunningInContainer() && fileExists("./config.yml") {
		configFilePath = "./config.yml"
		logger.Info("Found directly mounted config.yml in container current directory")
	} else {
		configFilePath = filepath.Join(configDir, "config.yml")
		logger.Info("Using config file path", "path", configFilePath)
	}

	// Add an indicator if this is not the first time we're loading config
	if configLogsPrinted {
		logger.Info("Reloading configuration (already loaded previously)...")
	}

	configExists := true
	var defaultsApplied bool = false

	logger.Debug("LoadConfig: Checking if config file exists", "path", configFilePath)
	_, err = os.Stat(configFilePath)
	if errors.Is(err, fs.ErrNotExist) {
		configExists = false
		logger.Info("Config file not found, creating it", "path", configFilePath)

		// Create config dir if it doesn't exist
		logger.Debug("LoadConfig: Creating config directory", "dir", configDir)
		err = os.MkdirAll(configDir, 0755)
		if err != nil {
			logger.Error("LoadConfig: Failed to create config directory", "error", err)
			return nil, fmt.Errorf("error creating configuration directory: %w", err)
		}

		// Create config file with the default values of ContainerEngineConfig
		logger.Debug("LoadConfig: Getting user home directory")
		homeDir, err := os.UserHomeDir()
		if err != nil {
			logger.Error("LoadConfig: Failed to get user home directory", "error", err)
			return nil, fmt.Errorf("could not get user home directory: %w", err)
		}

		// Check if running in container before creating the config
		logger.Debug("LoadConfig: Checking if running in container for podman setting")
		isInContainer := docker.IsRunningInContainer()
		logger.Info("Is running in container", "value", isInContainer)

		// Set default podman value based on environment
		defaultPodman := false
		// Check for non-interactive mode flag
		nonInteractive := os.Getenv("GORDON_NONINTERACTIVE") == "true"

		// Only prompt if not in a container and not in non-interactive mode
		if !isInContainer && !nonInteractive {
			logger.Info("Not in container, prompting for podman preference...")
			logger.Debug("LoadConfig: About to prompt for podman preference")
			defaultPodman = ReadUserInputNonBlocking("Are you using podman ? (y/n)", "n") == "y"
			logger.Debug("LoadConfig: User responded to podman prompt", "usingPodman", defaultPodman)
		} else if os.Getenv("GORDON_CONTAINER_PODMAN") == "true" {
			defaultPodman = true
			logger.Info("Podman container engine detected via environment variable")
		} else {
			logger.Info("Using Docker as the default container engine in container environment")
		}

		logger.Debug("LoadConfig: Creating default config structure")
		*config = Config{
			General: GeneralConfig{
				StorageDir: homeDir + "/.gordon",
				LogLevel:   defaultLogLevel,
			},
			Http: HttpConfig{
				Port:   "8080",
				Domain: "localhost",
				Https:  true,
			},
			Admin: AdminConfig{
				Path: "/admin",
			},
			ContainerEngine: ContainerEngineConfig{
				Sock:       sock,
				PodmanSock: podmansock,
				Podman:     defaultPodman,
				Network:    "gordon",
			},
			ReverseProxy: ReverseProxyConfig{
				Port:            reverseProxyPort,
				HttpPort:        httpPort,
				CertDir:         certDir,
				AutoRenew:       autoRenew,
				RenewBefore:     renewBefore,
				LetsEncryptMode: letsEncryptMode,
				CacheSize:       cacheSize,
				GracePeriod:     gracePeriod,
				EnableLogs:      true,
			},
		}

		// In this case, all defaults were applied since we created a new config
		defaultsApplied = true
	} else if err != nil {
		logger.Error("LoadConfig: Error checking if config file exists", "error", err)
		return nil, fmt.Errorf("error checking configuration file: %w", err)
	} else {
		// If config file exists, read it
		logger.Info("Reading existing configuration file...")
		logger.Debug("LoadConfig: About to read and unmarshal existing config file")
		err = readAndUnmarshalConfig(os.DirFS(filepath.Dir(configFilePath)), filepath.Base(configFilePath), config)
		if err != nil {
			logger.Error("LoadConfig: Failed to read/unmarshal config file", "error", err)
			return nil, err
		}
		logger.Debug("LoadConfig: Successfully read and unmarshaled config file")

		// Apply default values to fields that were not specified in the config file
		logger.Info("Applying default values to missing configuration fields...")
		defaultsApplied = applyDefaultsToConfig(config)
	}

	// Only load environment variables once, after either creating a new config or reading an existing one
	logger.Debug("LoadConfig: Checking if running in container for env vars")
	if docker.IsRunningInContainer() {
		// Only print logs if we haven't printed them before
		shouldPrintLogs := !configLogsPrinted
		logger.Info("Running in container, overriding configuration with environment variables...")
		logger.Debug("LoadConfig: About to load config from environment")
		loadConfigFromEnv(config, shouldPrintLogs)
		configLogsPrinted = true
		logger.Debug("LoadConfig: Finished loading config from environment")
	}

	// Load run env
	logger.Debug("LoadConfig: Setting run environment")
	config.Build.RunEnv = os.Getenv("RUN_ENV")
	logger.Info("Run environment", "env", config.Build.RunEnv)

	if config.Build.RunEnv == "" {
		config.Build.RunEnv = "prod"
		logger.Info("No RUN_ENV specified, defaulting to 'prod'")
	}

	// If we created a new config or applied defaults to an existing one, save it
	if !configExists || defaultsApplied {
		logger.Info("Saving configuration with default values...")
		logger.Debug("LoadConfig: About to save configuration with defaults")
		err = config.SaveConfig()
		if err != nil {
			logger.Error("LoadConfig: Failed to save configuration", "error", err)
			return nil, fmt.Errorf("error saving configuration: %w", err)
		}
		logger.Debug("LoadConfig: Successfully saved configuration with defaults")
	}

	// Debug output to verify config was loaded correctly
	// Only print this debug information if we're also printing the env var logs
	if !configLogsPrinted {
		logger.Info("Loaded configuration from", "path", configFilePath)
		logger.Info("Container engine config",
			"docker_socket", config.ContainerEngine.Sock,
			"podman_socket", config.ContainerEngine.PodmanSock,
			"using_podman", config.ContainerEngine.Podman,
			"network", config.ContainerEngine.Network)
	} else {
		// Just print a simplified message
		logger.Info("Loaded configuration from", "path", configFilePath)
	}

	// Apply the log level from configuration
	if config.General.LogLevel != "" {
		// Set the log level using our centralized logger
		logger.GetLogger().SetLogLevel(config.General.LogLevel)
		logger.Debug("Log level set from configuration", "level", config.General.LogLevel)
	}

	logger.Debug("LoadConfig: Completed LoadConfig function")
	return config, nil
}

func (config *Config) SaveConfig() error {
	configDir, err := getConfigDir()
	if err != nil {
		return fmt.Errorf("error getting configuration directory: %w", err)
	}

	// Check for directly mounted config.yml in root when in container
	var configFilePath string
	if docker.IsRunningInContainer() && fileExists("/config.yml") {
		configFilePath = "/config.yml"
	} else {
		configFilePath = filepath.Join(configDir, "config.yml")
	}

	logger.Info("Saving configuration to", "path", configFilePath)

	err = parser.WriteYAMLFile(configFilePath, config)
	if err != nil {
		return fmt.Errorf("error writing configuration file: %w", err)
	}

	return nil
}

// Helper function to check if a file exists
func fileExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return err == nil
}

func (config *Config) GetToken() (string, error) {
	token := config.General.Token
	if token == "" {
		return "", fmt.Errorf("no token found in config.yml")
	}

	return token, nil

}

// GetRunEnv returns the run environment
func (config *BuildConfig) GetRunEnv() string {
	return config.RunEnv
}

// get storage dir
func (config *Config) GetStorageDir() string {
	return config.General.StorageDir
}

func (c *Config) GetBackendURL() string {
	return c.Http.BackendURL
}

func (c *Config) SetToken(token string) {
	c.General.Token = token
}

func isWSL() bool {
	logger.Debug("isWSL: Checking if running in WSL")
	// Check if /proc/version exists
	_, err := os.Stat("/proc/version")
	if err != nil {
		logger.Debug("isWSL: /proc/version does not exist, not WSL", "error", err)
		return false
	}

	// Read /proc/version file
	logger.Debug("isWSL: Reading /proc/version file")
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		logger.Debug("isWSL: Failed to read /proc/version", "error", err)
		return false
	}

	// Convert to string and check if it contains "Microsoft" or "WSL"
	version := string(data)
	isWsl := strings.Contains(strings.ToLower(version), "microsoft") || strings.Contains(strings.ToLower(version), "wsl")
	logger.Debug("isWSL: WSL detection result", "isWsl", isWsl)
	return isWsl
}

func (c *HttpConfig) Protocol() string {
	if c.Https {
		return "https"
	}
	return "http"
}

func (c *HttpConfig) FullDomain() string {
	if c.SubDomain != "" {
		return fmt.Sprintf("%s.%s", c.SubDomain, c.Domain)
	}
	return c.Domain
}

// GetVersion
func (c *Config) GetVersion() string {
	return c.Build.BuildVersion
}
