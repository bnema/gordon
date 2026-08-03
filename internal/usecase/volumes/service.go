// Package volumes implements volume management operations.
package volumes

import (
	"context"
	"fmt"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Service implements the VolumeService interface.
type Service struct {
	runtime out.RuntimeVolumeManager
}

// NewService creates a new volume service.
func NewService(runtime out.ContainerRuntime) *Service {
	return NewServiceWithRuntimeVolumeManager(NewLocalRuntimeVolumeManager(runtime))
}

// NewServiceWithRuntimeVolumeManager creates a volume service backed by the narrow runtime volume port.
func NewServiceWithRuntimeVolumeManager(runtime out.RuntimeVolumeManager) *Service {
	return &Service{runtime: runtime}
}

// ListVolumes returns all volumes with usage status.
func (s *Service) ListVolumes(ctx context.Context) ([]*domain.VolumeInfo, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListVolumes",
	})

	vols, err := s.runtime.ListRuntimeVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}
	return vols, nil
}

// PruneVolumes removes volumes not mounted by any container.
// Returns a report and the list of volumes that were (or would be) removed.
func (s *Service) PruneVolumes(ctx context.Context, dryRun bool) (*domain.VolumePruneReport, []*domain.VolumeInfo, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "PruneVolumes",
		"dry_run":             dryRun,
	})
	log := zerowrap.FromCtx(ctx)

	vols, err := s.runtime.ListRuntimeVolumes(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	report := &domain.VolumePruneReport{}
	var removed []*domain.VolumeInfo

	for _, vol := range vols {
		if !isGordonManagedVolume(vol) || vol.InUse {
			continue
		}

		if !dryRun {
			if err := s.runtime.RemoveRuntimeVolume(ctx, vol.Name, false); err != nil {
				log.Warn().Err(err).Str("volume", vol.Name).Msg("failed to remove volume, skipping")
				continue
			}
		}

		removed = append(removed, vol)
		report.VolumesRemoved++
		report.SpaceReclaimed += vol.Size
	}

	return report, removed, nil
}

func isGordonManagedVolume(vol *domain.VolumeInfo) bool {
	if vol == nil || vol.Labels == nil {
		return false
	}
	return vol.Labels[domain.LabelManaged] == "true"
}
