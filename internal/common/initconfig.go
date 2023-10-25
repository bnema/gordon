package common

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"github.com/bnema/gordon/pkg/parser"
)

type Config struct {
	General         GeneralConfig         `yaml:"General"`
	Http            HttpConfig            `yaml:"Http"`
	Admin           AdminConfig           `yaml:"Admin"`
	ContainerEngine ContainerEngineConfig `yaml:"ContainerEngine"`
}

type AdminConfig struct {
	Path string `yaml:"path"`
}

type GeneralConfig struct {
	RunEnv       string // come from env
	BuildVersion string // come from env
	StorageDir   string `yaml:"storageDir"`
	Token        string `yaml:"token"`
}
type HttpConfig struct {
	Port       string `yaml:"port"`
	TopDomain  string `yaml:"topDomain"`
	SubDomain  string `yaml:"subDomain"`
	BackendURL string `yaml:"backendURL"`
}

type ContainerEngineConfig struct {
	Sock       string `yaml:"dockersock"`
	PodmanSock string `yaml:"podmansock"`
	Podman     bool   `yaml:"podman"`
	Network    string `yaml:"network"`
}

var (
	sock       = "/var/run/docker.sock"
	podmansock = "/run/user/1000/podman/podman.sock"
)

func isMaybeRunningInDocker() bool { // Thanks Pocketbase
	_, err := os.Stat("/.dockerenv")
	return err == nil
}

func getConfigDir() (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("error getting user's home directory: %w", err)
	}

	if isMaybeRunningInDocker() {
		return ".", nil // The directory where the binary is located
	}

	var configDir string
	if runtime.GOOS == "windows" {
		configDir = filepath.Join(usr.HomeDir, "AppData", "Roaming", "Gordon")
	} else {
		configDir = filepath.Join(usr.HomeDir, ".config", "Gordon")
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

	// Load env elements
	config.General.BuildVersion = os.Getenv("BUILD_VERSION")
	config.General.RunEnv = os.Getenv("RUN_ENV")

	// if RUN_ENV is not set, assume "prod" and config dir is the current dir
	if config.General.RunEnv == "" {
		config.General.RunEnv = "prod"
		configDir = "."
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

	fmt.Printf("Configuration saved to %s\n", configFilePath)
	return nil
}

func (config *Config) GetToken() (string, error) {
	token := config.General.Token
	if token == "" {
		return "", fmt.Errorf("no token found in config.yml")
	}

	return token, nil

}
