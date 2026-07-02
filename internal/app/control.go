package app

import (
	"fmt"
	"strings"
)

// LoadControlConfig loads only the control-plane targeting configuration.
func LoadControlConfig(configPath string) (ControlConfig, error) {
	_, cfg, err := initConfig(configPath)
	if err != nil {
		return ControlConfig{}, err
	}
	return cfg.Control, nil
}

// SplitModeEnabled reports whether configuration targets an external control plane.
func SplitModeEnabled(cfg Config) bool {
	return strings.TrimSpace(cfg.Control.Endpoint) != ""
}

func rejectSplitModeKernel(cfg Config) error {
	if !SplitModeEnabled(cfg) {
		return nil
	}
	return fmt.Errorf("local kernel initialization disabled: [control].endpoint is configured for split-mode control plane targeting (%q); use CLI remote/control targeting instead", cfg.Control.Endpoint)
}
