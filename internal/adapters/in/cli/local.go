// Package cli implements the CLI adapter for Gordon.
package cli

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	"gordon/internal/adapters/out/domainsecrets"
	"gordon/internal/app"
	"gordon/internal/boundaries/in"
	"gordon/internal/usecase/config"
	secretsSvc "gordon/internal/usecase/secrets"
)

// LocalServices provides direct access to local services for CLI operations.
type LocalServices struct {
	configSvc in.ConfigService
	secretSvc in.SecretService
	dataDir   string
}

// GetConfigService returns the config service.
func (l *LocalServices) GetConfigService() in.ConfigService {
	return l.configSvc
}

// GetSecretService returns the secret service.
func (l *LocalServices) GetSecretService() in.SecretService {
	return l.secretSvc
}

// GetDataDir returns the data directory.
func (l *LocalServices) GetDataDir() string {
	return l.dataDir
}

// GetLocalServices creates local services for CLI operations.
// It loads the config and initializes services without starting the server.
func GetLocalServices(configPath string) (*LocalServices, error) {
	// Set up viper with defaults
	v := viper.New()
	v.SetDefault("server.port", 80)
	v.SetDefault("server.registry_port", 5000)
	v.SetDefault("server.data_dir", app.DefaultDataDir())

	// Configure viper with config file paths
	app.ConfigureViper(v, configPath)

	// Read config file
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		// Config file not found is OK for some operations
	}

	// Create a minimal logger for CLI operations
	log := zerowrap.New(zerowrap.Config{
		Level:  "warn",
		Format: "console",
	})

	// Create config service (without event bus for CLI operations)
	configSvc := config.NewService(v, nil)
	ctx := context.Background()
	if err := configSvc.Load(ctx); err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	// Determine data directory and env directory
	dataDir := v.GetString("server.data_dir")
	if dataDir == "" {
		dataDir = app.DefaultDataDir()
	}

	envDir := v.GetString("env.dir")
	if envDir == "" {
		envDir = filepath.Join(dataDir, "env")
	}

	// Create domain secret store and service
	domainSecretStore, err := domainsecrets.NewFileStore(envDir, log)
	if err != nil {
		return nil, fmt.Errorf("failed to create domain secret store: %w", err)
	}
	secretSvc := secretsSvc.NewService(domainSecretStore, log)

	return &LocalServices{
		configSvc: configSvc,
		secretSvc: secretSvc,
		dataDir:   dataDir,
	}, nil
}
