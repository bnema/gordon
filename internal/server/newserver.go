package server

import (
	"io"
	"log"
	"time"

	"github.com/bnema/gordon/internal/common"
	"github.com/bnema/gordon/internal/templating"
	"github.com/bnema/gordon/internal/webui"
)

func NewServerApp() (*App, error) {
	// Initialize AppConfig
	config := common.Config{}
	_, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

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

	// Initialize DB
	DBDir := config.General.StorageDir + "/db"

	// Initialize App
	a := &App{
		TemplateFS: templating.TemplateFS,
		PublicFS:   webui.PublicFS,
		LocYML:     bytes,
		DBDir:      DBDir,
		DBFilename: DBFilename,
		Config:     config,
		StartTime:  time.Now(),
	}

	a.GenerateOauthCallbackURL()

	return a, nil
}
