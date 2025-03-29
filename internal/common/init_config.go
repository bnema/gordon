package common

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
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
	Path       string `yaml:"path"`
	AdminWebUI bool   `yaml:"-"`
}

type GeneralConfig struct {
	StorageDir        string `yaml:"storageDir"`
	LogLevel          string `yaml:"logLevel"`
	GordonContainerID string `yaml:"-"`
	JwtToken          string `yaml:"jwtToken"`
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
	Enabled                               bool   `yaml:"enabled"`              // Whether the reverse proxy is enabled
	Port                                  string `yaml:"port"`                 // Port for the reverse proxy to listen on
	HttpPort                              string `yaml:"httpPort"`             // HTTP port (usually 80) for redirecting to HTTPS
	CertDir                               string `yaml:"certDir"`              // Directory to store Let's Encrypt certificates
	Email                                 string `yaml:"email"`                // Email for Let's Encrypt
	LetsEncryptMode                       string `yaml:"letsEncryptMode"`      // staging or production
	SkipCertificates                      bool   `yaml:"skipCertificates"`     // Skip Let's Encrypt certificate acquisition when behind a TLS terminating proxy
	GracePeriod                           int    `yaml:"gracePeriod"`          // Shutdown grace period in seconds
	RenewBefore                           int    `yaml:"renewBefore"`          // Days before expiry to renew certificates
	EnableHttpLogs                        bool   `yaml:"enableHttpLogs"`       // Whether to enable HTTP request logging
	EnableRateLimit                       bool   `yaml:"enableRateLimit"`      // Whether to enable rate limiting middleware
	DetectUpstreamProxy                   bool   `yaml:"detectUpstreamProxy"`  // Whether to detect and handle upstream TLS termination proxies
	DefaultChallengeType                  string `yaml:"defaultChallengeType"` // http-01 or dns-01
	DefaultHttpChallengePort              string `yaml:"defaultHttpChallengePort"`
	DefaultDnsChallengePropagationTimeout int    `yaml:"defaultDnsChallengePropagationTimeout"`
	DefaultDnsChallengePollingInterval    int    `yaml:"defaultDnsChallengePollingInterval"`
}

// Default values
var (
	sock                                  = "/var/run/docker.sock"
	podmansock                            = "/run/podman/podman.sock"
	reverseProxyPort                      = "443" // Changed to 443 for standard HTTPS
	httpPort                              = "80"  // Standard HTTP port
	certDir                               = "/certs"
	letsEncryptMode                       = "staging"
	proxyEnabled                          = true   // Default to enabled for backward compatibility
	renewBefore                           = 30     // days
	gracePeriod                           = 30     // seconds
	defaultLogLevel                       = "info" // Default log level
	defaultHttpHttps                      = true   // Default to true
	disableRateLimit                      = false
	detectUpstreamProxy                   = false // Default to disabled
	skipCertificates                      = false // Default to disabled
	enableRateLimit                       = false // Default to disabled
	enableHttpLogs                        = true  // Default to enabled
	defaultChallengeType                  = "http-01"
	defaultHttpChallengePort              = "80"
	defaultDnsChallengePropagationTimeout = 60 // seconds
	defaultDnsChallengePollingInterval    = 5  // seconds
	defaultAdminPath                      = "/admin"
	defaultNetwork                        = "gordon"
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
	if config.General.StorageDir == "" {
		// When running in container, use /data as the default storage location
		if docker.IsRunningInContainer() {
			config.General.StorageDir = "/data"
		} else {
			// Otherwise, use ~/.gordon
			homeDir, err := os.UserHomeDir()
			if err != nil {
				homeDir = "/tmp"
			}
			config.General.StorageDir = filepath.Join(homeDir, ".gordon")
		}
		logger.Debug("Applied default value for General.StorageDir", "value", config.General.StorageDir)
		defaultsApplied = true
	}

	// Apply defaults to HttpConfig
	// Check if Https is NOT explicitly set to true (i.e., it's the zero value 'false')
	if !config.Http.Https {
		config.Http.Https = defaultHttpHttps // Use the defined default variable
		logger.Debug("Applied default value for Http.Https", "value", defaultHttpHttps)
		defaultsApplied = true
	}
	// No defaults for Http.Port, Http.Domain, Http.SubDomain as they require explicit config

	// Apply defaults to Admin config
	if config.Admin.Path == "" {
		config.Admin.Path = defaultAdminPath
		logger.Debug("Applied default value for Admin.Path", "value", defaultAdminPath)
		defaultsApplied = true
	}

	// Apply defaults to ContainerEngine config
	if config.ContainerEngine.Sock == "" {
		config.ContainerEngine.Sock = sock
		logger.Debug("Applied default value for ContainerEngine.Sock", "value", sock)
		defaultsApplied = true
	}
	if config.ContainerEngine.PodmanSock == "" {
		config.ContainerEngine.PodmanSock = podmansock
		logger.Debug("Applied default value for ContainerEngine.PodmanSock", "value", podmansock)
		defaultsApplied = true
	}
	if config.ContainerEngine.Network == "" {
		config.ContainerEngine.Network = defaultNetwork
		logger.Debug("Applied default value for ContainerEngine.Network", "value", defaultNetwork)
		defaultsApplied = true
	}

	// Apply defaults to ReverseProxy config
	// Set Enabled to true by default if not specified
	if !config.ReverseProxy.Enabled {
		config.ReverseProxy.Enabled = proxyEnabled
		logger.Debug("Applied default value for ReverseProxy.Enabled", "value", proxyEnabled)
		defaultsApplied = true
	}
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
	if config.ReverseProxy.GracePeriod == 0 {
		config.ReverseProxy.GracePeriod = gracePeriod
		logger.Debug("Applied default value for ReverseProxy.GracePeriod", "value", gracePeriod)
		defaultsApplied = true
	}
	// Check if "enableHttpLogs" is explicitly mentioned in the config file
	configFilePath := filepath.Join(getConfigDirMustExist(), "config.yml")
	if fileExists(configFilePath) {
		yamlContent, err := os.ReadFile(configFilePath)
		if err == nil {
			// Only set default if enableHttpLogs not explicitly specified
			if !strings.Contains(string(yamlContent), "enableHttpLogs:") {
				config.ReverseProxy.EnableHttpLogs = enableHttpLogs
				logger.Debug("Applied default value for ReverseProxy.EnableHttpLogs", "value", enableHttpLogs)
				defaultsApplied = true
			} else {
				logger.Debug("Keeping explicit value for ReverseProxy.EnableHttpLogs", "value", config.ReverseProxy.EnableHttpLogs)
			}
		} else {
			// If we can't read the file, apply the default only if not set
			if !config.ReverseProxy.EnableHttpLogs {
				config.ReverseProxy.EnableHttpLogs = enableHttpLogs
				logger.Debug("Applied default value for ReverseProxy.EnableHttpLogs", "value", enableHttpLogs)
				defaultsApplied = true
			}
		}
	} else {
		// If config file doesn't exist yet, set the default
		config.ReverseProxy.EnableHttpLogs = enableHttpLogs
		logger.Debug("Applied default value for ReverseProxy.EnableHttpLogs", "value", enableHttpLogs)
		defaultsApplied = true
	}
	// Set EnableRateLimit to false by default if not specified
	if !config.ReverseProxy.EnableRateLimit {
		config.ReverseProxy.EnableRateLimit = enableRateLimit
		logger.Debug("Applied default value for ReverseProxy.EnableRateLimit", "value", enableRateLimit)
		defaultsApplied = true
	}

	// Set DetectUpstreamProxy to false by default if not specified
	if !config.ReverseProxy.DetectUpstreamProxy {
		config.ReverseProxy.DetectUpstreamProxy = detectUpstreamProxy
		logger.Debug("Applied default value for ReverseProxy.DetectUpstreamProxy", "value", detectUpstreamProxy)
		defaultsApplied = true
	}

	// Set SkipCertificates to false by default if not specified
	if !config.ReverseProxy.SkipCertificates {
		config.ReverseProxy.SkipCertificates = skipCertificates
		logger.Debug("Applied default value for ReverseProxy.SkipCertificates", "value", skipCertificates)
		defaultsApplied = true
	}

	// Apply defaults for Let's Encrypt challenge settings
	if config.ReverseProxy.DefaultChallengeType == "" {
		config.ReverseProxy.DefaultChallengeType = defaultChallengeType
		logger.Debug("Applied default value for ReverseProxy.DefaultChallengeType", "value", defaultChallengeType)
		defaultsApplied = true
	}
	if config.ReverseProxy.DefaultHttpChallengePort == "" {
		config.ReverseProxy.DefaultHttpChallengePort = defaultHttpChallengePort
		logger.Debug("Applied default value for ReverseProxy.DefaultHttpChallengePort", "value", defaultHttpChallengePort)
		defaultsApplied = true
	}
	if config.ReverseProxy.DefaultDnsChallengePropagationTimeout == 0 {
		config.ReverseProxy.DefaultDnsChallengePropagationTimeout = defaultDnsChallengePropagationTimeout
		logger.Debug("Applied default value for ReverseProxy.DefaultDnsChallengePropagationTimeout", "value", defaultDnsChallengePropagationTimeout)
		defaultsApplied = true
	}
	if config.ReverseProxy.DefaultDnsChallengePollingInterval == 0 {
		config.ReverseProxy.DefaultDnsChallengePollingInterval = defaultDnsChallengePollingInterval
		logger.Debug("Applied default value for ReverseProxy.DefaultDnsChallengePollingInterval", "value", defaultDnsChallengePollingInterval)
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

// readAndUnmarshalConfig reads and unmarshals a configuration file using the provided filesystem and path.
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

func (config *Config) LoadConfig() (*Config, error) {
	// If we're in a non-interactive environment, use simplified config
	nonInteractiveEnv := os.Getenv("GORDON_NONINTERACTIVE")
	isNonInteractive := nonInteractiveEnv == "true" || nonInteractiveEnv == "1" || nonInteractiveEnv == "yes"
	isInContainer := docker.IsRunningInContainer() // Check once

	// Flag to track if saving is needed at the end
	shouldSaveConfig := false
	defaultsApplied := false

	if isNonInteractive {
		logger.Info("Non-interactive mode detected, using simplified config without prompts")
		// Start with an empty config
		*config = Config{}

		// Determine StorageDir
		if isInContainer {
			config.General.StorageDir = "/data"
			logger.Debug("Non-interactive: Using default container storage dir", "dir", config.General.StorageDir)
		} else {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				homeDir = "/tmp"
				logger.Warn("Could not get user home directory, using /tmp instead")
			}
			config.General.StorageDir = filepath.Join(homeDir, ".gordon")
			logger.Debug("Non-interactive: Using default host storage dir", "dir", config.General.StorageDir)
		}

		// Auto-detect Podman and set relevant fields
		isPodman, podmanSocket := docker.DetectPodman()
		config.ContainerEngine.Podman = isPodman
		if isPodman {
			config.ContainerEngine.PodmanSock = podmanSocket
			logger.Info("Non-interactive: Podman detected", "socket", podmanSocket)
		} else {
			// Explicitly set Docker sock if Podman not detected, applyDefaults might override if empty later
			config.ContainerEngine.Sock = sock
			logger.Info("Non-interactive: Assuming Docker")
		}

		// Apply remaining defaults
		defaultsApplied = applyDefaultsToConfig(config)
		if defaultsApplied {
			logger.Info("Non-interactive: Applied default configuration values")
			shouldSaveConfig = true // Mark for saving if defaults were needed
		}

		// If in container, apply env vars (potentially overriding defaults)
		if isInContainer {
			logger.Info("Non-interactive: Running in container, overriding configuration with environment variables...")
			// Assuming loadConfigFromEnv modifies config in place
			loadConfigFromEnv(config, !configLogsPrinted) // Pass flag to control initial logging
			configLogsPrinted = true                      // Mark logs as printed
			shouldSaveConfig = true                       // Mark for saving if env vars were applied
			logger.Debug("Non-interactive: Finished loading config from environment")
		}

		// Load run env (common step)
		config.Build.RunEnv = os.Getenv("RUN_ENV")
		if config.Build.RunEnv == "" {
			config.Build.RunEnv = "prod"
			logger.Info("Non-interactive: No RUN_ENV specified, defaulting to 'prod'")
		} else {
			logger.Info("Non-interactive: Run environment set", "env", config.Build.RunEnv)
		}

		// Check AdminWebUI (common step)
		if config.Admin.Path == "" {
			logger.Warn("Non-interactive: Admin path is empty or not set, admin dashboard webui will be disabled")
			config.Admin.AdminWebUI = false
		} else {
			config.Admin.AdminWebUI = true
		}

		// Apply log level (common step)
		if config.General.LogLevel != "" {
			logger.GetLogger().SetLogLevel(config.General.LogLevel)
			logger.Debug("Non-interactive: Log level set from configuration", "level", config.General.LogLevel)
		}

		// Save config if needed (only in non-interactive mode if defaults/env applied)
		if shouldSaveConfig {
			logger.Info("Non-interactive: Saving configuration...")
			err := config.SaveConfig()
			if err != nil {
				// Log error but continue, as config is loaded, just not saved
				logger.Error("Non-interactive: Failed to save configuration", "error", err)
			} else {
				logger.Info("Non-interactive: Successfully saved configuration")
			}
		}

		logger.Debug("LoadConfig: Completed non-interactive setup")
		return config, nil // Return early for non-interactive mode
	}

	// ---- Interactive Mode Logic ----
	logger.Debug("LoadConfig: Starting interactive LoadConfig function")

	// Log current execution environment for debugging
	cwd, _ := os.Getwd()
	logger.Debug("LoadConfig: Environment information",
		"isContainer", isInContainer, // Use cached value
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

	// Determine config file path
	var configFilePath string
	logger.Debug("LoadConfig: Checking config file location")
	// Check for direct mount first only if in container
	directMountPath := "./config.yml"
	useDirectMount := isInContainer && fileExists(directMountPath)

	if useDirectMount {
		configFilePath = directMountPath
		logger.Info("Found directly mounted config.yml in container current directory")
	} else {
		configFilePath = filepath.Join(configDir, "config.yml")
		logger.Info("Using config file path", "path", configFilePath)
	}

	// Add an indicator if this is not the first time we're loading config
	if configLogsPrinted {
		logger.Info("Reloading configuration (already loaded previously)...")
	}

	configExists := true // Assume exists unless stat fails

	logger.Debug("LoadConfig: Checking if config file exists", "path", configFilePath)
	_, err = os.Stat(configFilePath)
	if errors.Is(err, fs.ErrNotExist) {
		configExists = false
		shouldSaveConfig = true // Need to save if we create it
		logger.Info("Config file not found, creating it", "path", configFilePath)

		// Create config dir if it doesn't exist (only needed if using configDir path)
		if !useDirectMount {
			logger.Debug("LoadConfig: Creating config directory", "dir", configDir)
			err = os.MkdirAll(configDir, 0755)
			if err != nil {
				logger.Error("LoadConfig: Failed to create config directory", "error", err)
				return nil, fmt.Errorf("error creating configuration directory: %w", err)
			}
		} else {
			logger.Debug("LoadConfig: Using direct mount, skipping config directory creation")
		}

		// Start with an empty config
		*config = Config{}

		// Set specific initial values before applying defaults

		// Storage Directory
		if isInContainer {
			config.General.StorageDir = "/data"
			logger.Debug("LoadConfig: Setting initial StorageDir for container", "value", config.General.StorageDir)
		} else {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				logger.Error("LoadConfig: Failed to get user home directory", "error", err)
				// Setting a temporary default, applyDefaultsToConfig will handle it properly later if needed
				homeDir = "/tmp"
			}
			config.General.StorageDir = filepath.Join(homeDir, ".gordon")
			logger.Debug("LoadConfig: Setting initial StorageDir for host", "value", config.General.StorageDir)
		}

		// Podman Setting
		var defaultPodman bool
		if !isInContainer { // Only prompt if interactive and not in a container
			logger.Info("Not in container, prompting for podman preference...")
			logger.Debug("LoadConfig: About to prompt for podman preference")
			defaultPodman = ReadUserInputNonBlocking("Are you using podman ? (y/n)", "n") == "y"
			logger.Debug("LoadConfig: User responded to podman prompt", "usingPodman", defaultPodman)
		} else if os.Getenv("GORDON_CONTAINER_PODMAN") == "true" {
			defaultPodman = true
			logger.Info("Podman container engine detected via environment variable")
		} else {
			// Default to false (Docker) in container if env var not set
			defaultPodman = false
			logger.Info("Using Docker as the default container engine in container environment")
		}
		config.ContainerEngine.Podman = defaultPodman
		logger.Debug("LoadConfig: Set initial podman value", "value", defaultPodman)

		// Now apply all other defaults
		logger.Info("Applying default values for the new configuration...")
		defaultsApplied = applyDefaultsToConfig(config)
		// defaultsApplied is already true implicitly because we created the file

	} else if err != nil {
		logger.Error("LoadConfig: Error checking if config file exists", "error", err)
		return nil, fmt.Errorf("error checking configuration file: %w", err)
	} else {
		// If config file exists, read it
		logger.Info("Reading existing configuration file...")
		logger.Debug("LoadConfig: About to read and unmarshal existing config file")
		// Use DirFS based on whether it's a direct mount or in configDir
		var baseDir string
		var fileName string
		if useDirectMount {
			baseDir = "."
			fileName = "config.yml"
		} else {
			baseDir = configDir
			fileName = "config.yml"
		}
		err = readAndUnmarshalConfig(os.DirFS(baseDir), fileName, config)
		if err != nil {
			logger.Error("LoadConfig: Failed to read/unmarshal config file", "error", err)
			return nil, err
		}
		logger.Debug("LoadConfig: Successfully read and unmarshaled config file")

		// Apply default values to fields that were not specified in the config file
		logger.Info("Applying default values to missing configuration fields...")
		defaultsApplied = applyDefaultsToConfig(config)
		if defaultsApplied {
			shouldSaveConfig = true // Mark for saving if defaults were applied to existing file
		}
	}

	// --- Common Steps for Interactive Mode (after file read/creation) ---

	// Load environment variables if in a container (might override file/defaults)
	envVarsLoaded := false
	if isInContainer {
		shouldPrintLogs := !configLogsPrinted
		logger.Info("Running in container, overriding configuration with environment variables...")
		logger.Debug("LoadConfig: About to load config from environment")
		loadConfigFromEnv(config, shouldPrintLogs) // Assuming modification in place
		envVarsLoaded = true
		configLogsPrinted = true
		shouldSaveConfig = true // Mark for saving if env vars were applied
		logger.Debug("LoadConfig: Finished loading config from environment")
	}

	// Load run env
	logger.Debug("LoadConfig: Setting run environment")
	config.Build.RunEnv = os.Getenv("RUN_ENV")
	if config.Build.RunEnv == "" {
		config.Build.RunEnv = "prod"
		logger.Info("No RUN_ENV specified, defaulting to 'prod'")
	} else {
		logger.Info("Run environment set", "env", config.Build.RunEnv)
	}

	// Check AdminWebUI status
	if config.Admin.Path == "" {
		logger.Warn("Admin path is empty or not set, admin dashboard webui will be disabled")
		config.Admin.AdminWebUI = false
	} else {
		config.Admin.AdminWebUI = true
	}

	// Save config file if it was created, defaults were applied, or env vars loaded in container
	if shouldSaveConfig {
		logger.Info("Saving final configuration state...", "path", configFilePath)
		logger.Debug("LoadConfig: About to save configuration", "reason", fmt.Sprintf("configExists=%v, defaultsApplied=%v, envVarsLoadedInContainer=%v", !configExists, defaultsApplied, envVarsLoaded && isInContainer))
		err = config.SaveConfig() // SaveConfig determines the correct path internally now
		if err != nil {
			// Log error but proceed, config is loaded, just potentially not saved
			logger.Error("LoadConfig: Failed to save final configuration", "error", err)
		} else {
			logger.Info("LoadConfig: Successfully saved final configuration")
		}
	}

	// Apply the log level from the final configuration
	if config.General.LogLevel != "" {
		logger.GetLogger().SetLogLevel(config.General.LogLevel)
		logger.Debug("Log level set from final configuration", "level", config.General.LogLevel)
	} else {
		logger.Warn("Final configuration has empty LogLevel")
	}

	// Final debug log before returning
	logger.Debug("LoadConfig: Completed interactive LoadConfig function")
	return config, nil
}

func (config *Config) SaveConfig() error {
	configDir, err := getConfigDir()
	if err != nil {
		return fmt.Errorf("error getting configuration directory: %w", err)
	}
	isInContainer := docker.IsRunningInContainer()

	// Determine the correct save path
	var configFilePath string
	directMountPath := "/config.yml" // Use root path for direct container mount check
	if isInContainer && fileExists(directMountPath) {
		configFilePath = directMountPath
		logger.Debug("SaveConfig: Using direct mount path", "path", configFilePath)
	} else if isInContainer && fileExists("./config.yml") { // Check relative path if direct root mount check fails
		configFilePath = "./config.yml"
		logger.Debug("SaveConfig: Using relative path in container", "path", configFilePath)
	} else {
		// Default to XDG path if not a direct mount or relative path found in container
		configFilePath = filepath.Join(configDir, "config.yml")
		logger.Debug("SaveConfig: Using standard config directory path", "path", configFilePath)

		// Ensure the directory exists before trying to write the file
		err = os.MkdirAll(filepath.Dir(configFilePath), 0755)
		if err != nil {
			return fmt.Errorf("error ensuring configuration directory exists for saving: %w", err)
		}
	}

	logger.Info("Saving configuration to", "path", configFilePath)

	// Create a temporary config copy excluding Build field for saving
	saveConfig := *config
	saveConfig.Build = BuildConfig{} // Exclude build info from saved file

	err = parser.WriteYAMLFile(configFilePath, &saveConfig) // Pass pointer to the copy
	if err != nil {
		return fmt.Errorf("error writing configuration file: %w", err)
	}

	logger.Debug("SaveConfig: Configuration saved successfully", "path", configFilePath)
	return nil
}

// Helper function to check if a file exists
func fileExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return err == nil
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

// GetToken returns the token
func (c *Config) GetToken() string {
	return c.General.JwtToken
}

func (c *HttpConfig) Protocol() string {
	if c.Https {
		return "https"
	}
	return "http"
}

func (c *HttpConfig) FullDomain() string {
	domain := c.Domain

	// Clean the domain to ensure it doesn't contain path components
	domain = strings.TrimSuffix(domain, "/")
	if idx := strings.Index(domain, "/"); idx >= 0 {
		// If there's a path component, remove it
		domain = domain[:idx]
	}

	if c.SubDomain != "" {
		// Clean the subdomain too
		subdomain := strings.TrimSuffix(c.SubDomain, "/")
		return fmt.Sprintf("%s.%s", subdomain, domain)
	}
	return domain
}

// GetVersion
func (c *Config) GetVersion() string {
	return c.Build.BuildVersion
}

// IsAdminWebUIEnabled returns whether the admin dashboard is enabled
func (c *AdminConfig) IsAdminWebUIEnabled() bool {
	return c.AdminWebUI && c.Path != ""
}
