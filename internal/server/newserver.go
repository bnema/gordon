package server

import (
	"fmt"
	"io"
	"log"
	"time"

	"github.com/bnema/gordon/internal/templating"
	"github.com/bnema/gordon/internal/webui"
)

func NewServerApp() *App {
	// Initialize AppConfig
	config := &Config{}
	_, err := LoadConfig(config)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Append build dir to storage dir
	config.General.StorageDir = fmt.Sprintf("%s/%s", config.General.BuildDir, config.General.StorageDir)

	// Open the strings.yml file containing the strings for the current language
	file, err := templating.TemplateFS.Open("locstrings.yml")
	if err != nil {
		log.Fatalf("Failed to open strings.yml: %v", err)
	}

	// Read the file content into a byte slice
	bytes, err := io.ReadAll(file)
	if err != nil {
		log.Fatalf("Failed to read strings.yml: %v", err)
	}

	file.Close()

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

	OauthCallbackURL = config.GenerateOauthCallbackURL()
	return a
}
