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

func NewClientApp() *App {
	// Initialize AppConfig
	config := &Config{}
	config, err := config.LoadClientConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Initialize App
	a := &App{
		Config: *config,
	}
	return a
}

func (config *Config) LoadClientConfig() (*Config, error) {
	configFileName := "config.yml"
	usr, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("error getting user's home directory: %w", err)
	}

	var configDir string

	if runtime.GOOS == "windows" {
		configDir = filepath.Join(usr.HomeDir, "AppData", "Roaming", "Gordon")
	} else {
		configDir = filepath.Join(usr.HomeDir, ".config", "Gordon")
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("error creating configuration directory: %w", err)
	}

	configFilePath := filepath.Join(configDir, configFileName)

	if _, err := os.Stat(configFilePath); os.IsNotExist(err) {
		fmt.Printf("Config file not found, creating it at %s\n", configFilePath)
		defaultConfig := []byte("some: default\ndata: here\n")
		if err := os.WriteFile(configFilePath, defaultConfig, 0666); err != nil {
			return nil, fmt.Errorf("error writing default configuration: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("error checking configuration file: %w", err)
	}

	// Read and unmarshal the config file
	configData, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, fmt.Errorf("error reading configuration file: %w", err)
	}

	err = yaml.Unmarshal(configData, &config)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling configuration file: %w", err)
	}

	// New logic for backend URL and token
	needsSave := false

	if config.Http.BackendURL == "" {
		fmt.Println("Please enter the backend URL:")
		var backendURL string
		fmt.Scanln(&backendURL)
		config.Http.BackendURL = backendURL
		needsSave = true
	}

	if config.General.GordonToken == "" {
		fmt.Println("Please enter the token:")
		var token string
		fmt.Scanln(&token)
		config.General.GordonToken = token
		needsSave = true
	}

	if needsSave {
		err := config.SaveConfig() // Assuming SaveConfig is a method that saves the config to file
		if err != nil {
			return nil, fmt.Errorf("error saving configuration file: %w", err)
		}
	}

	return config, nil
}

func (config *Config) SaveConfig() error {
	usr, err := user.Current()
	if err != nil {
		return fmt.Errorf("error getting user's home directory: %w", err)
	}

	var configDir string
	if runtime.GOOS == "windows" {
		configDir = filepath.Join(usr.HomeDir, "AppData", "Roaming", "Gordon")
	} else {
		configDir = filepath.Join(usr.HomeDir, ".config", "Gordon")
	}

	configFilePath := filepath.Join(configDir, "config.yml")

	// Marshal the config struct into YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %v", err)
	}

	// Write the data to the config file
	err = os.WriteFile(configFilePath, data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write to config file: %v", err)
	}

	return nil
}
