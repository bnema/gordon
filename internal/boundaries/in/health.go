// Package in defines input ports (interfaces) for use cases.
// These interfaces define the contract between driving adapters (HTTP, CLI)
// and the business logic (use cases).
package in

import (
	"context"

	"gordon/internal/domain"
)

// HealthService defines the contract for route health checking operations.
type HealthService interface {
	// CheckRoute performs a health check on a single route.
	// It checks both container status and HTTP reachability.
	CheckRoute(ctx context.Context, route domain.Route) *domain.RouteHealth

	// CheckAllRoutes performs health checks on all configured routes.
	// Returns a map of domain to health status.
	CheckAllRoutes(ctx context.Context) map[string]*domain.RouteHealth
}

// HTTPProber defines the contract for HTTP health probing.
// This allows for easy mocking in tests.
type HTTPProber interface {
	// Probe sends an HTTP request to the URL and returns status code and response time.
	// Returns (statusCode, responseTimeMs, error).
	Probe(ctx context.Context, url string) (int, int64, error)
}
