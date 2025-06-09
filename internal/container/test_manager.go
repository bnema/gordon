package container

import (
	"gordon/internal/config"
	"gordon/internal/env"
	"gordon/internal/logging"
	"gordon/pkg/runtime"
)

// NewManagerWithRuntime creates a manager with an injected runtime for testing
func NewManagerWithRuntime(cfg *config.Config, rt runtime.Runtime) (*Manager, error) {
	// Create environment loader
	envLoader := env.NewLoader(cfg)

	// Register secret providers based on config
	for _, providerName := range cfg.Env.Providers {
		switch providerName {
		case "pass":
			envLoader.RegisterSecretProvider("pass", env.NewPassProvider())
		case "sops":
			envLoader.RegisterSecretProvider("sops", env.NewSopsProvider())
		}
	}

	// Create log manager  
	logManager := logging.NewLogManager(cfg, rt)

	return &Manager{
		runtime:    rt,
		config:     cfg,
		containers: make(map[string]*runtime.Container),
		envLoader:  envLoader,
		logManager: logManager,
	}, nil
}