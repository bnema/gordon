// Package in defines input ports (interfaces) for use cases.
// These interfaces define the contract between driving adapters (HTTP, CLI)
// and the business logic (use cases).
package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// ContainerService defines the contract for container runtime reads and
// lifecycle retained by the v2.50 cutover. The route-container engine
// (deploy/restart/remove/reconcile/attachments/sync/autostart) was
// removed with the declarative-apps cutover; the deployment engine owns
// workload effects through out.ContainerRuntime directly.
type ContainerService interface {
	// ListNetworks returns Gordon-managed networks.
	ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error)

	// Shutdown gracefully shuts down all managed containers.
	Shutdown(ctx context.Context) error
}
