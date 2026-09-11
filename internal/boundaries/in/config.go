package in

import (
	"context"
)

// ConfigService defines the contract for installation configuration.
// Application state (routes, attachments, networks, auto, preview) was
// removed with the declarative-apps cutover: presence fails startup and
// reload closed with a config-retired diagnostic, and app data flows
// through ACTIVE state, never here.
type ConfigService interface {
	// Load loads the configuration from the configured source.
	Load(ctx context.Context) error

	// Reload re-reads the configuration file from disk and loads it into memory.
	// This is different from Load() which only loads from the cached viper values.
	Reload(ctx context.Context) error

	// Watch starts watching for configuration changes.
	// The onChange callback is called when configuration changes are detected.
	Watch(ctx context.Context, onChange func()) error

	// GetServerPort returns the configured server port.
	GetServerPort() int

	// GetRegistryPort returns the configured registry port.
	GetRegistryPort() int

	// GetRegistryDomain returns the configured registry domain.
	GetRegistryDomain() string

	// GetDataDir returns the configured data directory.
	GetDataDir() string

	// IsNetworkIsolationEnabled returns whether network isolation is enabled.
	IsNetworkIsolationEnabled() bool

	// GetNetworkPrefix returns the prefix for created networks.
	GetNetworkPrefix() string

	// GetExternalRoutes returns all configured external routes.
	GetExternalRoutes() map[string]string
}
