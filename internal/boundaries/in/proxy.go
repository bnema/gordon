package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// ProxyService defines the contract for proxy routing and state management.
// HTTP request handling is the adapter's responsibility (adapters/in/http/proxy).
type ProxyService interface {
	// GetTarget returns the proxy target for a given domain.
	GetTarget(ctx context.Context, domain string) (*domain.ProxyTarget, error)

	// RegisterTarget registers a new proxy target for a domain.
	RegisterTarget(ctx context.Context, domain string, target *domain.ProxyTarget) error

	// UnregisterTarget removes a proxy target for a domain.
	UnregisterTarget(ctx context.Context, domain string) error

	// RefreshTargets refreshes all proxy targets from container state.
	RefreshTargets(ctx context.Context) error

	// IsRegistryDomain returns true if the host matches the configured registry domain.
	IsRegistryDomain(host string) bool

	// IsKnownHost returns true if the host is configured as registry, route, or external route.
	IsKnownHost(ctx context.Context, host string) bool

	// TrackInFlight records an in-flight request for a container.
	// Returns a release function that must be called when the request completes.
	TrackInFlight(containerID string) func()

	// TrackRegistryRequest increments the registry in-flight counter.
	TrackRegistryRequest()

	// ReleaseRegistryRequest decrements the registry in-flight counter.
	ReleaseRegistryRequest()

	// ProxyConfig returns the current proxy configuration.
	// The adapter uses this for HTTP-level enforcement (body size, response size, concurrency).
	ProxyConfig() ProxyServiceConfig
}

// ProxyServiceConfig holds configuration that the proxy adapter needs
// from the usecase layer for HTTP-level enforcement.
type ProxyServiceConfig struct {
	RegistryDomain     string
	RegistryPort       int
	MaxBodySize        int64
	MaxResponseSize    int64
	MaxConcurrentConns int
}
