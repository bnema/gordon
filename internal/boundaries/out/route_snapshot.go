package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// RouteSnapshotProvider provides the current edge route target snapshot.
type RouteSnapshotProvider interface {
	CurrentSnapshot(ctx context.Context) (domain.RouteTargetSnapshot, error)
}

// RouteSnapshotWatcher streams route target snapshots as they change.
type RouteSnapshotWatcher interface {
	WatchSnapshots(ctx context.Context) (<-chan domain.RouteTargetSnapshot, error)
}

// EdgeDrainCoordinator coordinates narrow edge drain acknowledgements.
type EdgeDrainCoordinator interface {
	AcknowledgeDrain(ctx context.Context, routeDomain string, targetKey domain.RouteTargetKey, generation domain.RouteTargetGeneration) error
}
