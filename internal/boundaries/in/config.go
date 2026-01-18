package in

import (
	"context"

	"gordon/internal/domain"
)

// ConfigService defines the contract for configuration management.
type ConfigService interface {
	// Load loads the configuration from the configured source.
	Load(ctx context.Context) error

	// Reload re-reads the configuration file from disk and loads it into memory.
	// This is different from Load() which only loads from the cached viper values.
	Reload(ctx context.Context) error

	// GetRoutes returns all configured routes.
	GetRoutes(ctx context.Context) []domain.Route

	// GetRoute returns a single route by domain.
	GetRoute(ctx context.Context, domain string) (*domain.Route, error)

	// AddRoute adds a new route to the configuration.
	AddRoute(ctx context.Context, route domain.Route) error

	// UpdateRoute updates an existing route.
	UpdateRoute(ctx context.Context, route domain.Route) error

	// RemoveRoute removes a route from the configuration.
	RemoveRoute(ctx context.Context, domain string) error

	// Save persists the current configuration to disk.
	Save(ctx context.Context) error

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

	// IsAutoRouteEnabled returns whether auto-route is enabled.
	IsAutoRouteEnabled() bool

	// IsNetworkIsolationEnabled returns whether network isolation is enabled.
	IsNetworkIsolationEnabled() bool

	// GetNetworkPrefix returns the prefix for created networks.
	GetNetworkPrefix() string

	// GetExternalRoutes returns all configured external routes.
	GetExternalRoutes() map[string]string

	// GetAllAttachments returns all configured attachments.
	GetAllAttachments(ctx context.Context) map[string][]string

	// GetAttachmentsFor returns attachments for a specific domain or network group.
	GetAttachmentsFor(ctx context.Context, domainOrGroup string) ([]string, error)

	// AddAttachment adds an image to a domain/group's attachments.
	AddAttachment(ctx context.Context, domainOrGroup, image string) error

	// RemoveAttachment removes an image from a domain/group's attachments.
	RemoveAttachment(ctx context.Context, domainOrGroup, image string) error
}
