package common

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bnema/gordon/pkg/docker"
	"github.com/bnema/gordon/pkg/parser"
)

type Config struct {
	General         GeneralConfig         `yaml:"General"`
	Http            HttpConfig            `yaml:"Http"`
	Admin           AdminConfig           `yaml:"Admin"`
	ContainerEngine ContainerEngineConfig `yaml:"ContainerEngine"`
	Traefik         TraefikConfig         `yaml:"Traefik"`
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

type TraefikConfig struct {
	EntryPoint       string `yaml:"entryPoint"`
	SecureEntryPoint string `yaml:"secureEntryPoint"`
	Resolver         string `yaml:"resolver"`
}

// Default values
var (
	sock             = "/var/run/docker.sock"
	podmansock       = "/run/user/1000/podman/podman.sock"
	entryPoint       = "web"
	entryPointSecure = "websecure"
	resolver         = "myresolver"
)

func getConfigDir() (string, error) {

	if docker.IsRunningInContainer() {
		return "/.", nil
	}

	// Get the user's home directory for non-container environments
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("error getting user's home directory: %w", err)
	}

	// Select the configuration directory based on the OS
	configDir := filepath.Join(homeDir, ".config", "Gordon")
	if runtime.GOOS == "windows" {
		configDir = filepath.Join(homeDir, "AppData", "Roaming", "Gordon")
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

func (config *Config) LoadConfig() (*Config, error) {
	configDir, err := getConfigDir()
	if err != nil {
		return nil, fmt.Errorf("error getting configuration directory: %w", err)
	}

	configFilePath := filepath.Join(configDir, "config.yml")

	_, err = os.Stat(configFilePath)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Printf("Config file not found, creating it at %s\n", configFilePath)

		// Create config dir if it doesn't exist
		err = os.MkdirAll(configDir, 0755)
		if err != nil {
			return nil, fmt.Errorf("error creating configuration directory: %w", err)
		}

		// Create config file with the default values of ContainerEngineConfig
		config = &Config{
			ContainerEngine: ContainerEngineConfig{
				Sock:       sock,
				PodmanSock: podmansock,
				Podman:     ReadUserInput("Are you using podman ? (y/n)") == "y",
			},
			Traefik: TraefikConfig{
				EntryPoint:       entryPoint,
				SecureEntryPoint: entryPointSecure,
				Resolver:         resolver,
			},
		}

		err = config.SaveConfig()
		if err != nil {
			return nil, fmt.Errorf("error saving new configuration: %w", err)
		}
		return config, nil
	}

	if err != nil {
		return nil, fmt.Errorf("error checking configuration file: %w", err)
	}

	err = readAndUnmarshalConfig(os.DirFS(configDir), "config.yml", config)
	if err != nil {
		return nil, err
	}

	// Load run env
	config.Build.RunEnv = os.Getenv("RUN_ENV")

	if config.Build.RunEnv == "" {
		config.Build.RunEnv = "prod"
	}

	return config, nil
}

func (config *Config) SaveConfig() error {
	configDir, err := getConfigDir()
	if err != nil {
		return fmt.Errorf("error getting configuration directory: %w", err)
	}
	configFilePath := filepath.Join(configDir, "config.yml")

	err = parser.WriteYAMLFile(configFilePath, config)
	if err != nil {
		return fmt.Errorf("error writing configuration file: %w", err)
	}

	return nil
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

// GetVersion returns the version
func (config *Config) GetVersion() string {
	return config.Build.BuildVersion
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
