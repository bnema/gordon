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
	disableRateLimit                      = false
	detectUpstreamProxy                   = false // Default to disabled
	skipCertificates                      = false // Default to disabled
	enableRateLimit                       = false // Default to disabled
	defaultChallengeType                  = "http-01"
	defaultHttpChallengePort              = "80"
	defaultDnsChallengePropagationTimeout = 60 // seconds
	defaultDnsChallengePollingInterval    = 5  // seconds
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

	// Apply defaults to Admin config
	if config.Admin.Path == "" {
		config.Admin.Path = "/admin"
		logger.Debug("Applied default value for Admin.Path", "value", "/admin")
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
		config.ContainerEngine.Network = "gordon"
		logger.Debug("Applied default value for ContainerEngine.Network", "value", "gordon")
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
				config.ReverseProxy.EnableHttpLogs = true
				logger.Debug("Applied default value for ReverseProxy.EnableHttpLogs", "value", true)
				defaultsApplied = true
			} else {
				logger.Debug("Keeping explicit value for ReverseProxy.EnableHttpLogs", "value", config.ReverseProxy.EnableHttpLogs)
			}
		} else {
			// If we can't read the file, apply the default only if not set
			if !config.ReverseProxy.EnableHttpLogs {
				config.ReverseProxy.EnableHttpLogs = true
				logger.Debug("Applied default value for ReverseProxy.EnableHttpLogs", "value", true)
				defaultsApplied = true
			}
		}
	} else {
		// If config file doesn't exist yet, set the default
		config.ReverseProxy.EnableHttpLogs = true
		logger.Debug("Applied default value for ReverseProxy.EnableHttpLogs", "value", true)
		defaultsApplied = true
	}
	// Set EnableRateLimit to false by default if not specified
	if !config.ReverseProxy.EnableRateLimit {
		config.ReverseProxy.EnableRateLimit = false
		logger.Debug("Applied default value for ReverseProxy.EnableRateLimit", "value", false)
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
				RenewBefore:     renewBefore,
				LetsEncryptMode: letsEncryptMode,
				GracePeriod:     gracePeriod,
				EnableHttpLogs:  true,
				EnableRateLimit: false,
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
				RenewBefore:     renewBefore,
				LetsEncryptMode: letsEncryptMode,
				GracePeriod:     gracePeriod,
				EnableHttpLogs:  true,
				EnableRateLimit: false,
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

	// After loading from file or environment, check if Admin.Path is empty
	// This should be placed before returning the config
	if config.Admin.Path == "" {
		logger.Warn("Admin path is empty or not set, admin dashboard webui will be disabled")
		config.Admin.AdminWebUI = false
	} else {
		config.Admin.AdminWebUI = true
	}

	// Save config file if running in container and env vars were applied
	// This ensures config.yml always reflects the environment variables
	if docker.IsRunningInContainer() && configLogsPrinted {
		logger.Info("Environment variables were applied, saving configuration to reflect current values...")
		err = config.SaveConfig()
		if err != nil {
			logger.Error("Failed to save configuration with environment variables", "error", err)
		} else {
			logger.Info("Successfully updated config.yml with environment variable values")
		}
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
