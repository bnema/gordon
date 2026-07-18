package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	edgesnapshotusecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

// migrationAppliedStateReceiver persists an edge acknowledgement only after
// transport authentication and tracker validation. The persisted component ID
// binds the generation to the deterministic edge in the prepared checkpoint,
// allowing a restarted control process to retain the cutover fact.
type migrationAppliedStateReceiver struct {
	mu      sync.Mutex
	store   *MigrationCheckpointStore
	tracker *edgesnapshotusecase.AppliedStateTracker
}

func newMigrationAppliedStateReceiver(store *MigrationCheckpointStore, tracker *edgesnapshotusecase.AppliedStateTracker) (*migrationAppliedStateReceiver, error) {
	if store == nil || tracker == nil {
		return nil, fmt.Errorf("migration checkpoint store and edge applied-state tracker are required")
	}
	return &migrationAppliedStateReceiver{store: store, tracker: tracker}, nil
}

func (r *migrationAppliedStateReceiver) ReportAuthenticatedAppliedState(ctx context.Context, identity string, state edgesnapshotusecase.AppliedState) error {
	if r == nil || r.store == nil || r.tracker == nil {
		return fmt.Errorf("migration edge applied-state receiver is not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	checkpoint, err := r.store.Load()
	if errors.Is(err, os.ErrNotExist) {
		// Normal split edge operation has no migration checkpoint. It still
		// benefits from authenticated in-memory readiness, but has nothing to
		// persist for a monolith retry.
		return r.tracker.ReportAuthenticatedAppliedState(ctx, identity, state)
	}
	if err != nil {
		return fmt.Errorf("load migration checkpoint: %w", err)
	}
	return r.persistAuthenticatedAppliedState(ctx, checkpoint, identity, state)
}

func (r *migrationAppliedStateReceiver) persistAuthenticatedAppliedState(ctx context.Context, checkpoint *MigrationCheckpoint, identity string, state edgesnapshotusecase.AppliedState) error {
	edge, err := migrationCheckpointEdge(*checkpoint)
	if err != nil {
		return err
	}
	if identity != edge.ComponentID || state.ComponentID != edge.ComponentID || checkpoint.AppliedEdgeComponentID != "" && checkpoint.AppliedEdgeComponentID != edge.ComponentID {
		return edgesnapshotusecase.ErrAppliedStateUnexpected
	}
	if checkpoint.RouteSnapshotGeneration > state.RouteGeneration || checkpoint.RouteSnapshotGeneration > state.TrafficGeneration {
		return edgesnapshotusecase.ErrAppliedStateStale
	}
	if err := r.tracker.ReportAuthenticatedAppliedState(ctx, identity, state); err != nil {
		return err
	}
	checkpoint.RouteSnapshotGeneration = state.RouteGeneration
	checkpoint.AppliedEdgeComponentID = edge.ComponentID
	if err := r.store.Save(*checkpoint); err != nil {
		return fmt.Errorf("persist authenticated edge applied state: %w", err)
	}
	return nil
}

func migrationCheckpointEdge(checkpoint MigrationCheckpoint) (ComponentLaunchComponent, error) {
	if checkpoint.Phase != MigrationPhasePrepared {
		return ComponentLaunchComponent{}, edgesnapshotusecase.ErrAppliedStateUnexpected
	}
	plan, err := NewComponentLaunchPlan(checkpoint)
	if err != nil {
		return ComponentLaunchComponent{}, fmt.Errorf("read migration edge identity: %w", err)
	}
	edge, found := componentForRole(plan, "edge")
	if !found {
		return ComponentLaunchComponent{}, edgesnapshotusecase.ErrAppliedStateUnexpected
	}
	return edge, nil
}
