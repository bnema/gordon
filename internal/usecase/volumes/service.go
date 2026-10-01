// Package volumes implements volume management operations.
package volumes

import (
	"context"
	"fmt"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/pruneguard"
)

// Service implements the VolumeService interface.
type Service struct {
	runtime out.ContainerRuntime
	// protection, pruneRuntime, and barrier are the prune ports. All
	// three are required for PruneVolumes; a missing port fails closed.
	protection   out.PruneProtectionStore
	pruneRuntime out.PruneRuntime
	barrier      out.GCBarrier
}

// NewService creates a new volume service.
func NewService(runtime out.ContainerRuntime) *Service {
	return &Service{runtime: runtime}
}

// WithPrunePorts wires the prune ports: the coherent protection store,
// the runtime inventory/deletion port, and the GC barrier.
func (s *Service) WithPrunePorts(protection out.PruneProtectionStore, pruneRuntime out.PruneRuntime, barrier out.GCBarrier) *Service {
	s.protection = protection
	s.pruneRuntime = pruneRuntime
	s.barrier = barrier
	return s
}

// ListVolumes returns all volumes with usage status.
func (s *Service) ListVolumes(ctx context.Context) ([]*domain.VolumeInfo, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListVolumes",
	})

	vols, err := s.runtime.ListVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}
	return vols, nil
}

// PruneVolumes plans (and unless dryRun executes) one volume prune run.
//
// A volume is removed only when a durable ownership record explicitly
// records it as released, the runtime labels agree with that record, and
// no container uses it. Every other volume is protected or unknown.
// A valid plan with zero eligible volumes succeeds: with current
// metadata a zero-deletion volume prune is a normal outcome.
func (s *Service) PruneVolumes(ctx context.Context, dryRun bool) (*domain.VolumePruneReport, []*domain.VolumeInfo, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "PruneVolumes",
		"dry_run":             dryRun,
	})
	log := zerowrap.FromCtx(ctx)

	lease, err := s.acquireExclusive(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("volumes: acquire prune lease: %w", err)
	}
	defer lease.Release()

	if s.protection == nil || s.pruneRuntime == nil {
		return nil, nil, fmt.Errorf("volumes: prune requires a protection store and a runtime inventory port: %w", domain.ErrPruneDisabled)
	}

	snapshot, err := s.protection.ProtectionSnapshot(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("volumes: read protection snapshot: %w", err)
	}

	inventory, err := s.pruneRuntime.InventoryRuntime(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("volumes: read runtime inventory: %w", err)
	}

	plan := &domain.PrunePlan{
		Gaps: append(append([]domain.InventoryGap(nil), snapshot.Gaps...), inventory.Gaps...),
		Volumes: pruneguard.PlanVolumes(pruneguard.VolumePlanInput{
			Volumes:  inventory.Volumes,
			Snapshot: snapshot,
			Complete: inventory.Complete(),
		}),
	}
	if err := plan.Validate(); err != nil {
		return nil, nil, fmt.Errorf("volumes: refusing to execute an invalid prune plan: %w", err)
	}

	report := &domain.VolumePruneReport{Plan: *domain.NewPruneReport(plan, !dryRun)}
	if dryRun {
		return report, nil, nil
	}

	byName := make(map[string]*domain.VolumeInfo, len(inventory.Volumes))
	for _, volume := range inventory.Volumes {
		if volume != nil {
			byName[volume.Name] = volume
		}
	}

	var removed []*domain.VolumeInfo
	for _, ref := range plan.EligibleVolumes() {
		if err := s.pruneRuntime.RemoveVolumeExact(ctx, ref); err != nil {
			log.Warn().Err(err).Str("volume", ref.Name).Msg("failed to remove volume, skipping")
			report.Plan.Failures = append(report.Plan.Failures, domain.PruneFailure{
				Kind: domain.PruneResourceVolume, Ref: ref.Name, Err: err.Error(),
			})
			continue
		}
		report.Plan.Deleted = append(report.Plan.Deleted, domain.PruneCandidateReport{
			Kind: domain.PruneResourceVolume, Ref: ref.Name, Verdict: domain.PruneVerdictEligible,
		})
		report.VolumesRemoved++
		if volume, ok := byName[ref.Name]; ok {
			report.SpaceReclaimed += volume.Size
			report.Plan.ReclaimedBytes += volume.Size
			// The driver reported this size for the volume it removed.
			report.Plan.ReclaimedKnown = true
			removed = append(removed, volume)
			continue
		}
		removed = append(removed, &domain.VolumeInfo{Name: ref.Name})
	}

	return report, removed, nil
}

// acquireExclusive takes the GC exclusive lease for the whole prune run,
// or a no-op lease when no barrier is wired.
func (s *Service) acquireExclusive(ctx context.Context) (out.GCLease, error) {
	if s.barrier == nil {
		return noopGCLease{}, nil
	}
	return s.barrier.AcquireExclusive(ctx)
}

// noopGCLease is the lease used when no barrier is wired.
type noopGCLease struct{}

func (noopGCLease) Release() {}
