package app

import (
	"fmt"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

// NewClientApp initializes a new App with configuration.
func NewClientApp() (*App, error) {
	config := &Config{}
	config, err := config.LoadClientConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}
	return &App{Config: *config}, nil
}

// getConfigDir returns the configuration directory based on the operating system.
func getConfigDir() (string, error) {
	usr, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("error getting user's home directory: %w", err)
	}

	var configDir string
	if runtime.GOOS == "windows" {
		configDir = filepath.Join(usr.HomeDir, "AppData", "Roaming", "Gordon")
	} else {
		configDir = filepath.Join(usr.HomeDir, ".config", "Gordon")
	}
	return configDir, nil
}

// configPath returns the path of the configuration directory and file.
func configPath() (string, string) {
	configDir, err := getConfigDir()
	if err != nil {
		log.Fatalf("error getting configuration directory: %v", err)
	}
	configFilePath := filepath.Join(configDir, "config.yml")
	return configDir, configFilePath
}

// SaveConfig saves the current configuration to a file.
func (config *Config) SaveConfig() error {
	configDir, err := getConfigDir()
	if err != nil {
		return err
	}
	configFilePath := filepath.Join(configDir, "config.yml")
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}
	if err := os.WriteFile(configFilePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write to config file: %v", err)
	}
	return nil
}

// readAndUnmarshalConfig reads the config file and unmarshals it into the given config.
func readAndUnmarshalConfig(filePath string, config *Config) error {
	configData, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("error reading configuration file: %w", err)
	}
	return yaml.Unmarshal(configData, config)
}

func (config *Config) LoadClientConfig() (*Config, error) {
	configDir, configFilePath := configPath()

	err := os.MkdirAll(configDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("error creating configuration directory: %w", err)
	}

	_, err = os.Stat(configFilePath)
	if os.IsNotExist(err) {
		fmt.Printf("Config file not found, creating it at %s\n", configFilePath)

		config.Http.BackendURL = readUserInput("Enter the backend URL (e.g. https://gordon.mydomain.com):")
		config.General.GordonToken = readUserInput("Enter the token (check your backend config.yml):")

		err = config.SaveConfig()
		if err != nil {
			return nil, fmt.Errorf("error saving new configuration: %w", err)
		}

		return config, nil
	}

	if err != nil {
		return nil, fmt.Errorf("error checking configuration file: %w", err)
	}

	err = readAndUnmarshalConfig(configFilePath, config)
	if err != nil {
		return nil, err
	}

	return config, nil
}

// readUserInput reads a string input from the user with a prompt.
func readUserInput(prompt string) string {
	fmt.Println(prompt)
	var input string
	fmt.Scanln(&input)
	return input
}
