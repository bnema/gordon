package out

import (
	"context"
	"errors"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// ErrRouteSnapshotUnavailable indicates that an edge has not received a
// route snapshot yet.
var ErrRouteSnapshotUnavailable = errors.New("route snapshot is unavailable")

// RouteSnapshotErrorCategory is a sanitized health error classification.
// It never contains transport addresses, credentials, or server error text.
type RouteSnapshotErrorCategory string

const (
	RouteSnapshotErrorNone           RouteSnapshotErrorCategory = ""
	RouteSnapshotErrorTransport      RouteSnapshotErrorCategory = "transport"
	RouteSnapshotErrorAuthentication RouteSnapshotErrorCategory = "authentication"
	RouteSnapshotErrorInvalid        RouteSnapshotErrorCategory = "invalid_snapshot"
)

// RouteSnapshotHealth is an immutable, sanitized view of a snapshot stream.
type RouteSnapshotHealth struct {
	Healthy                bool
	Connected              bool
	LastAcceptedGeneration domain.RouteTargetGeneration
	LastUpdate             time.Time
	ErrorCategory          RouteSnapshotErrorCategory
}

// RouteSnapshotProvider provides the current edge route target snapshot.
// Implementations MUST return an independent Clone; published snapshots are immutable.
type RouteSnapshotProvider interface {
	CurrentSnapshot(ctx context.Context) (domain.RouteTargetSnapshot, error)
}

// RouteSnapshotHealthReporter reports sanitized stream health. It deliberately
// excludes endpoint, credential, and raw transport error details.
type RouteSnapshotHealthReporter interface {
	SnapshotHealth() RouteSnapshotHealth
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

// EdgeDrainReporter reports one opaque retired-target drain state to control.
// It is intentionally unable to carry backing/container identity.
type EdgeDrainReporter interface {
	ReportDrainState(ctx context.Context, state domain.RouteDrainState) error
}

// RouteSnapshotAcceptanceObserver is called exactly once for each strictly
// newer validated snapshot accepted by an edge snapshot consumer. previous is
// nil for the initial snapshot; callers must not retain either value.
type RouteSnapshotAcceptanceObserver interface {
	ObserveAcceptedRouteSnapshot(previous *domain.RouteTargetSnapshot, current domain.RouteTargetSnapshot)
}
