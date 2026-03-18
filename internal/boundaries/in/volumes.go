package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// VolumeService defines volume listing and prune operations.
type VolumeService interface {
	// ListVolumes returns all volumes with usage status.
	ListVolumes(ctx context.Context) ([]*domain.VolumeInfo, error)

	// PruneVolumes removes volumes not mounted by any container.
	// If dryRun is true, returns what would be removed without removing.
	PruneVolumes(ctx context.Context, dryRun bool) (*domain.VolumePruneReport, []*domain.VolumeInfo, error)
}
