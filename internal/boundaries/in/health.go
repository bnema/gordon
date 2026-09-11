// Package in defines input ports (interfaces) for use cases.
// These interfaces define the contract between driving adapters (HTTP, CLI)
// and the business logic (use cases).
package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// HealthService defines the contract for app health checking operations.
type HealthService interface {
	// CheckAllRoutes performs health checks on all app-served HTTP hosts.
	// Returns a map of canonical host to health status.
	CheckAllRoutes(ctx context.Context) map[string]*domain.RouteHealth
}

// HTTPProber defines the contract for HTTP health probing.
// This allows for easy mocking in tests.
type HTTPProber interface {
	// Probe sends an HTTP request to the URL and returns status code and response time.
	// Returns (statusCode, responseTimeMs, error).
	Probe(ctx context.Context, url string) (int, int64, error)
}
