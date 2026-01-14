// Package in defines input ports (interfaces) for use cases.
// These interfaces define the contract between driving adapters (HTTP, CLI)
// and the business logic (use cases).
package in

import (
	"context"

	"gordon/internal/domain"
)

// ContainerService defines the contract for container management operations.
type ContainerService interface {
	// Deploy creates and starts a container for the given route.
	Deploy(ctx context.Context, route domain.Route) (*domain.Container, error)

	// Stop stops a running container.
	Stop(ctx context.Context, containerID string) error

	// Remove removes a container, optionally forcing removal.
	Remove(ctx context.Context, containerID string, force bool) error

	// Get retrieves a container by domain name.
	Get(ctx context.Context, domain string) (*domain.Container, bool)

	// List returns all managed containers.
	List(ctx context.Context) map[string]*domain.Container

	// HealthCheck performs health checks on all containers.
	HealthCheck(ctx context.Context) map[string]bool

	// SyncContainers synchronizes containers with configured routes.
	SyncContainers(ctx context.Context) error

	// AutoStart starts containers for all configured routes.
	AutoStart(ctx context.Context) error

	// Shutdown gracefully shuts down all managed containers.
	Shutdown(ctx context.Context) error
}
