package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// RouteSnapshotProvider provides the current edge route target snapshot.
// Implementations MUST return an independent Clone; published snapshots are immutable.
type RouteSnapshotProvider interface {
	CurrentSnapshot(ctx context.Context) (domain.RouteTargetSnapshot, error)
}

// RouteSnapshotWatcher streams route target snapshots as they change.
//
// On success WatchSnapshots MUST return a nonnil receive-only channel. The producer owns
// and closes that channel promptly when ctx is cancelled; consumers never close it. Every
// emitted snapshot MUST be an independent Clone and is immutable after publication. A
// post-subscription error MUST terminate and close the channel and be surfaced through the
// implementation's health/logging mechanism (Phase 4).
type RouteSnapshotWatcher interface {
	WatchSnapshots(ctx context.Context) (<-chan domain.RouteTargetSnapshot, error)
}

// EdgeDrainCoordinator coordinates narrow edge drain acknowledgements.
// Implementations MUST canonicalize and validate routeDomain, targetKey, and generation
// before acknowledging a drain.
type EdgeDrainCoordinator interface {
	AcknowledgeDrain(ctx context.Context, routeDomain string, targetKey domain.RouteTargetKey, generation domain.RouteTargetGeneration) error
}
