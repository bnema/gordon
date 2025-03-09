package server

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/proxy"
	"github.com/bnema/gordon/internal/templating"
	"github.com/bnema/gordon/internal/webui"
	"github.com/charmbracelet/log"
)

func NewServerApp(buildConfig *common.BuildConfig) (*App, error) {
	// Get global config singleton instead of loading the config again
	config, err := common.GetGlobalConfig(buildConfig)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Info("Starting Gordon server", "version", config.Build.BuildVersion)

	// For development environment, use the project root directory
	if config.Build.RunEnv == "dev" {
		// Get the current working directory as the project root
		projectRoot, err := os.Getwd()
		if err != nil {
			log.Fatalf("Failed to get current working directory: %v", err)
		}

		// Set storage directory to project root + /data instead of ~/.gordon
		config.General.StorageDir = filepath.Join(projectRoot, "data")
		log.Debug("Dev environment detected, using local storage path", "path", config.General.StorageDir)

		// Create the directory if it doesn't exist
		if err := os.MkdirAll(config.General.StorageDir, 0755); err != nil {
			log.Fatalf("Failed to create storage directory: %v", err)
		}
	}

	// Open the strings.yml file containing the strings for the current language
	file, err := templating.TemplateFS.Open("txt/locstrings.yml")
	if err != nil {
		log.Fatalf("Failed to open strings.yml: %v", err)
	}

	// Read the file content into a byte slice
	bytes, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("Failed to read strings.yml: %v", err)
	}

	file.Close()

	// Initialize DB
	DBDir := config.General.StorageDir + "/db"

	// Initialize App
	a := &App{
		TemplateFS: templating.TemplateFS,
		PublicFS:   webui.PublicFS,
		LocYML:     bytes,
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
	log.Info("Initializing reverse proxy")

	// Create a new proxy
	p, err := proxy.NewProxy(a)
	if err != nil {
		return nil, err
	}

	// Start the proxy
	if err := p.Start(); err != nil {
		return nil, err
	}

	log.Info("Reverse proxy initialized and started")
	return p, nil
}
