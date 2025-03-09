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

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/parser"
)

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
	StorageDir string `yaml:"storageDir"`
	Token      string `yaml:"token"`
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
}

// Default values
var (
	sock             = "/var/run/docker.sock"
	podmansock       = "/run/user/1000/podman/podman.sock"
	reverseProxyPort = "443" // Changed to 443 for standard HTTPS
	httpPort         = "80"  // Standard HTTP port
	certDir          = "/certs"
	letsEncryptMode  = "staging"
	autoRenew        = true
	renewBefore      = 30   // days
	cacheSize        = 1000 // entries
	gracePeriod      = 30   // seconds
)

func getConfigDir() (string, error) {
	var configDir string

	if isWSL() {
		// Use XDG_CONFIG_HOME for WSL
		if xdgConfigHome := os.Getenv("XDG_CONFIG_HOME"); xdgConfigHome != "" {
			configDir = filepath.Join(xdgConfigHome, "Gordon")
		} else {
			// If XDG_CONFIG_HOME is not set, fall back to default locations
			homeDir, err := os.UserHomeDir()
			if err != nil {
				// If we can't get the home directory, use a fallback
				homeDir = os.TempDir() // Use the system's temp directory as a fallback
				fmt.Printf("Warning: Unable to determine home directory. Using temp directory: %s\n", homeDir)
			}

			configDir = filepath.Join(homeDir, ".config", "Gordon")

		}

		return configDir, nil
	}

	if docker.IsRunningInContainer() {
		return "/", nil
	}

	// Check for XDG_CONFIG_HOME first
	if xdgConfigHome := os.Getenv("XDG_CONFIG_HOME"); xdgConfigHome != "" {
		configDir = filepath.Join(xdgConfigHome, "Gordon")
	} else {
		// If XDG_CONFIG_HOME is not set, fall back to default locations
		homeDir, err := os.UserHomeDir()
		if err != nil {
			// If we can't get the home directory, use a fallback
			homeDir = os.TempDir() // Use the system's temp directory as a fallback
			fmt.Printf("Warning: Unable to determine home directory. Using temp directory: %s\n", homeDir)
		}

		// Select the configuration directory based on the OS
		if runtime.GOOS == "windows" {
			configDir = filepath.Join(homeDir, "AppData", "Roaming", "Gordon")
		} else {
			configDir = filepath.Join(homeDir, ".config", "Gordon")
		}
	}

	return configDir, nil
}

func readAndUnmarshalConfig(fs fs.FS, filePath string, config *Config) error {
	err := parser.ParseYAMLFile(fs, filePath, config)
	if err != nil {
		return fmt.Errorf("error reading and unmarshaling configuration file: %w", err)
	}
	return nil
}

// loadConfigFromEnv loads configuration from environment variables
func loadConfigFromEnv(config *Config) {
	// General Configuration
	if val := os.Getenv("GORDON_STORAGE_DIR"); val != "" {
		config.General.StorageDir = val
		fmt.Printf("Using environment variable GORDON_STORAGE_DIR: %s\n", val)
	}
	if val := os.Getenv("GORDON_TOKEN"); val != "" {
		config.General.Token = val
		fmt.Printf("Using environment variable GORDON_TOKEN\n")
	}

	// HTTP Configuration
	if val := os.Getenv("GORDON_HTTP_PORT"); val != "" {
		config.Http.Port = val
		fmt.Printf("Using environment variable GORDON_HTTP_PORT: %s\n", val)
	}
	if val := os.Getenv("GORDON_HTTP_DOMAIN"); val != "" {
		config.Http.Domain = val
		fmt.Printf("Using environment variable GORDON_HTTP_DOMAIN: %s\n", val)
	}
	if val := os.Getenv("GORDON_HTTP_SUBDOMAIN"); val != "" {
		config.Http.SubDomain = val
		fmt.Printf("Using environment variable GORDON_HTTP_SUBDOMAIN: %s\n", val)
	}
	if val := os.Getenv("GORDON_HTTP_BACKEND_URL"); val != "" {
		config.Http.BackendURL = val
		fmt.Printf("Using environment variable GORDON_HTTP_BACKEND_URL: %s\n", val)
	}
	if val := os.Getenv("GORDON_HTTP_HTTPS"); val != "" {
		config.Http.Https = strings.ToLower(val) == "true"
		fmt.Printf("Using environment variable GORDON_HTTP_HTTPS: %t\n", config.Http.Https)
	}

	// Admin Configuration
	if val := os.Getenv("GORDON_ADMIN_PATH"); val != "" {
		config.Admin.Path = val
		fmt.Printf("Using environment variable GORDON_ADMIN_PATH: %s\n", val)
	}

	// Container Engine Configuration
	if val := os.Getenv("GORDON_CONTAINER_SOCK"); val != "" {
		config.ContainerEngine.Sock = val
		fmt.Printf("Using environment variable GORDON_CONTAINER_SOCK: %s\n", val)
	}
	if val := os.Getenv("GORDON_CONTAINER_PODMAN_SOCK"); val != "" {
		config.ContainerEngine.PodmanSock = val
		fmt.Printf("Using environment variable GORDON_CONTAINER_PODMAN_SOCK: %s\n", val)
	}
	if val := os.Getenv("GORDON_CONTAINER_PODMAN"); val != "" {
		config.ContainerEngine.Podman = strings.ToLower(val) == "true"
		fmt.Printf("Using environment variable GORDON_CONTAINER_PODMAN: %t\n", config.ContainerEngine.Podman)

		// If using Podman, and Sock is empty but PodmanSock is set, use PodmanSock for Sock field
		if config.ContainerEngine.Podman && config.ContainerEngine.Sock == "" && config.ContainerEngine.PodmanSock != "" {
			config.ContainerEngine.Sock = config.ContainerEngine.PodmanSock
			fmt.Printf("Setting ContainerEngine.Sock to PodmanSock value: %s\n", config.ContainerEngine.Sock)
		}
	}
	if val := os.Getenv("GORDON_CONTAINER_NETWORK"); val != "" {
		config.ContainerEngine.Network = val
		fmt.Printf("Using environment variable GORDON_CONTAINER_NETWORK: %s\n", val)
	}

	// If using Docker socket with podman-compose mounting
	if config.ContainerEngine.Sock == "" && fileExists("/var/run/docker.sock") {
		config.ContainerEngine.Sock = "/var/run/docker.sock"
		fmt.Printf("Docker socket found at /var/run/docker.sock, using it\n")
	}

	// Reverse Proxy Configuration
	if val := os.Getenv("GORDON_PROXY_PORT"); val != "" {
		config.ReverseProxy.Port = val
		fmt.Printf("Using environment variable GORDON_PROXY_PORT: %s\n", val)
	}
	if val := os.Getenv("GORDON_PROXY_HTTP_PORT"); val != "" {
		config.ReverseProxy.HttpPort = val
		fmt.Printf("Using environment variable GORDON_PROXY_HTTP_PORT: %s\n", val)
	}
	if val := os.Getenv("GORDON_PROXY_CERT_DIR"); val != "" {
		config.ReverseProxy.CertDir = val
		fmt.Printf("Using environment variable GORDON_PROXY_CERT_DIR: %s\n", val)
	}
	if val := os.Getenv("GORDON_PROXY_AUTO_RENEW"); val != "" {
		config.ReverseProxy.AutoRenew = strings.ToLower(val) == "true"
		fmt.Printf("Using environment variable GORDON_PROXY_AUTO_RENEW: %t\n", config.ReverseProxy.AutoRenew)
	}
	if val := os.Getenv("GORDON_PROXY_RENEW_BEFORE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.ReverseProxy.RenewBefore = i
			fmt.Printf("Using environment variable GORDON_PROXY_RENEW_BEFORE: %d\n", i)
		}
	}
	if val := os.Getenv("GORDON_PROXY_LETSENCRYPT_MODE"); val != "" {
		config.ReverseProxy.LetsEncryptMode = val
		fmt.Printf("Using environment variable GORDON_PROXY_LETSENCRYPT_MODE: %s\n", val)
	}
	if val := os.Getenv("GORDON_PROXY_EMAIL"); val != "" {
		config.ReverseProxy.Email = val
		fmt.Printf("Using environment variable GORDON_PROXY_EMAIL: %s\n", val)
	}
	if val := os.Getenv("GORDON_PROXY_CACHE_SIZE"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.ReverseProxy.CacheSize = i
			fmt.Printf("Using environment variable GORDON_PROXY_CACHE_SIZE: %d\n", i)
		}
	}
	if val := os.Getenv("GORDON_PROXY_GRACE_PERIOD"); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			config.ReverseProxy.GracePeriod = i
			fmt.Printf("Using environment variable GORDON_PROXY_GRACE_PERIOD: %d\n", i)
		}
	}
}

func (config *Config) LoadConfig() (*Config, error) {
	configDir, err := getConfigDir()
	if err != nil {
		return nil, fmt.Errorf("error getting configuration directory: %w", err)
	}

	// Check for directly mounted config.yml in root when in container
	var configFilePath string
	if docker.IsRunningInContainer() && fileExists("/config.yml") {
		configFilePath = "/config.yml"
		fmt.Println("Found directly mounted config.yml in container root")
	} else {
		configFilePath = filepath.Join(configDir, "config.yml")
	}

	configExists := true
	_, err = os.Stat(configFilePath)
	if errors.Is(err, fs.ErrNotExist) {
		configExists = false
		fmt.Printf("Config file not found, creating it at %s\n", configFilePath)

		// Create config dir if it doesn't exist
		err = os.MkdirAll(configDir, 0755)
		if err != nil {
			return nil, fmt.Errorf("error creating configuration directory: %w", err)
		}

		// Create config file with the default values of ContainerEngineConfig
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("could not get user home directory: %w", err)
		}

		*config = Config{
			General: GeneralConfig{
				StorageDir: homeDir + "/.gordon",
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
				Podman:     ReadUserInput("Are you using podman ? (y/n)") == "y",
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
			},
		}

		// If running in a container, skip the prompt
		if docker.IsRunningInContainer() {
			config.ContainerEngine.Podman = false
			// Handle the Podman case if the GORDON_CONTAINER_PODMAN env var is set to true
			if os.Getenv("GORDON_CONTAINER_PODMAN") == "true" {
				config.ContainerEngine.Podman = true
				fmt.Println("Podman container engine detected via environment variable")
			}
		}
	} else if err != nil {
		return nil, fmt.Errorf("error checking configuration file: %w", err)
	}

	// If config file exists, read it
	if configExists {
		err = readAndUnmarshalConfig(os.DirFS(filepath.Dir(configFilePath)), filepath.Base(configFilePath), config)
		if err != nil {
			return nil, err
		}
	}

	// Only load environment variables once, after either creating a new config or reading an existing one
	if docker.IsRunningInContainer() {
		fmt.Println("Running in container, overriding configuration with environment variables...")
		loadConfigFromEnv(config)
	}

	// Load run env
	config.Build.RunEnv = os.Getenv("RUN_ENV")

	if config.Build.RunEnv == "" {
		config.Build.RunEnv = "prod"
	}

	// If we created a new config, save it
	if !configExists {
		err = config.SaveConfig()
		if err != nil {
			return nil, fmt.Errorf("error saving new configuration: %w", err)
		}
	}

	// Debug output to verify config was loaded correctly
	fmt.Printf("Loaded configuration from %s\n", configFilePath)
	fmt.Printf("Container engine config - Docker socket: %s, Podman socket: %s, Using Podman: %t, Network: %s\n",
		config.ContainerEngine.Sock,
		config.ContainerEngine.PodmanSock,
		config.ContainerEngine.Podman,
		config.ContainerEngine.Network)

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

	fmt.Printf("Saving configuration to %s\n", configFilePath)

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
	// Check for /proc/version file
	if _, err := os.Stat("/proc/version"); err == nil {
		content, err := os.ReadFile("/proc/version")
		if err == nil && strings.Contains(strings.ToLower(string(content)), "microsoft") {
			return true
		}
	}

	// Check for WSL-specific environment variable
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}

	return false
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
