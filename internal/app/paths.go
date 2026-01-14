// Package app provides the application initialization and wiring.
package app

import (
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// DefaultDataDir returns the default data directory path.
// Uses ~/.gordon for user installations, /var/lib/gordon as fallback.
func DefaultDataDir() string {
	if homeDir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(homeDir, ".gordon")
	}
	return "/var/lib/gordon"
}

// ConfigureViper sets up viper with standard config file search paths.
// Config file: gordon.toml
// Search paths (in order): /etc/gordon, ~/.config/gordon, current directory
func ConfigureViper(v *viper.Viper, configPath string) {
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		v.SetConfigName("gordon")
		v.SetConfigType("toml")
		v.AddConfigPath("/etc/gordon")
		v.AddConfigPath("$HOME/.config/gordon")
		v.AddConfigPath(".")
	}
}
