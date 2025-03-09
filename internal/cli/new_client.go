package cli

import (
	"fmt"

	"github.com/bnema/gordon/internal/common"
)

type App struct {
	Config common.Config
}

// NewClientApp initializes a new App with configuration.
func NewClientApp(buildConfig *common.BuildConfig) (*App, error) {
	// Get global config singleton instead of loading the config again
	config, err := common.GetGlobalConfig(buildConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to load configuration: %w", err)
	}

	// Initialize App
	a := &App{
		Config: *config,
	}

	return a, nil
}
