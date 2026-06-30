// Package in defines input ports (interfaces) for use cases.
// These interfaces define the contract between driving adapters (HTTP, CLI)
// and the business logic (use cases).
package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// StandaloneServiceService defines standalone service lifecycle operations.
type StandaloneServiceService interface {
	Reconcile(ctx context.Context, services []domain.StandaloneService) error
	Status(ctx context.Context) ([]domain.StandaloneServiceStatus, error)
}
