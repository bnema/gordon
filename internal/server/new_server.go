package server

import (
	"os"
	"path/filepath"
	"time"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/proxy"
	"github.com/bnema/gordon/internal/templating"
	"github.com/bnema/gordon/pkg/logger"
	"gopkg.in/yaml.v3"
)

func NewServerApp(buildConfig *common.BuildConfig) (*App, error) {
	// Get global config singleton instead of loading the config again
	config, err := common.GetGlobalConfig(buildConfig)
	if err != nil {
		logger.Fatalf("Failed to load configuration: %v", err)
	}

	logger.Info("Starting Gordon server", "version", config.Build.BuildVersion)

	// For development environment, use the project root directory
	if config.Build.RunEnv == "dev" {
		// Get the current working directory as the project root
		projectRoot, err := os.Getwd()
		if err != nil {
			logger.Fatalf("Failed to get current working directory: %v", err)
		}

		// Set storage directory to project root + /data instead of ~/.gordon
		config.General.StorageDir = filepath.Join(projectRoot, "data")
		logger.Debug("Dev environment detected, using local storage path", "path", config.General.StorageDir)

		// Create the directory if it doesn't exist
		if err := os.MkdirAll(config.General.StorageDir, 0755); err != nil {
			logger.Fatalf("Failed to create storage directory: %v", err)
		}
	}

	// Read the embedded localization file
	locBytes, err := templating.LocFS.ReadFile("models/txt/locstrings.yml")
	if err != nil {
		logger.Fatalf("Failed to read embedded locstrings.yml: %v", err)
	}

	// Parse the YAML content
	var stringsMap map[string]interface{}
	if err := yaml.Unmarshal(locBytes, &stringsMap); err != nil {
		logger.Fatalf("Failed to parse locstrings.yml: %v", err)
	}

	// Initialize DB
	DBDir := config.General.StorageDir + "/db"

	// Initialize App
	a := &App{
		Strings:    stringsMap,
		DBDir:      DBDir,
		DBFilename: DBFilename,
		Config:     *config,
		StartTime:  time.Now(),
	}

	a.GenerateOauthCallbackURL()

	return a, nil
}

// InitializeProxy initializes and starts the reverse proxy
func (a *App) InitializeProxy() (*proxy.Proxy, error) {
	logger.Info("Initializing reverse proxy")

	// Create a new proxy
	p, err := proxy.NewProxy(a)
	if err != nil {
		return nil, err
	}

	// Start the proxy
	if err := p.Start(); err != nil {
		return nil, err
	}

	logger.Info("Reverse proxy initialized and started")
	return p, nil
}
