package cli

import (
	"fmt"

	"github.com/bnema/gordon/internal/common"
)

type App struct {
	Config common.Config
}

// NewClientApp initializes a new App with configuration.
func NewClientApp(buildConfig common.BuildConfig) (*App, error) {
	// Initialize AppConfig
	config := common.Config{
		Build: buildConfig,
	}
	_, err := config.LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize App
	a := &App{
		Config: config,
	}

	return a, nil
}
