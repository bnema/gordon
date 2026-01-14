package in

import (
	"context"
	"net/http"

	"gordon/internal/domain"
)

// ProxyService defines the contract for reverse proxy operations.
type ProxyService interface {
	// ServeHTTP handles incoming HTTP requests and proxies them to the appropriate backend.
	ServeHTTP(w http.ResponseWriter, r *http.Request)

	// GetTarget returns the proxy target for a given domain.
	GetTarget(ctx context.Context, domain string) (*domain.ProxyTarget, error)

	// RegisterTarget registers a new proxy target for a domain.
	RegisterTarget(ctx context.Context, domain string, target *domain.ProxyTarget) error

	// UnregisterTarget removes a proxy target for a domain.
	UnregisterTarget(ctx context.Context, domain string) error

	// RefreshTargets refreshes all proxy targets from container state.
	RefreshTargets(ctx context.Context) error
}
