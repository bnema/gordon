package out

import (
	"context"
	"io"

	"github.com/bnema/gordon/internal/domain"
)

// RuntimeCommandClient sends narrow runtime intent commands to a runtime worker.
type RuntimeCommandClient interface {
	DeployRoute(ctx context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error)
	RestartRoute(ctx context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error)
	RemoveRoute(ctx context.Context, command domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error)
	Reconcile(ctx context.Context, command domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error)
}

// RuntimeStatePublisher publishes sanitized runtime actual-state snapshots.
type RuntimeStatePublisher interface {
	PublishRuntimeState(ctx context.Context, snapshot domain.RuntimeActualStateSnapshot) error
}

// RuntimeStateSubscriber subscribes to sanitized runtime actual-state snapshots.
type RuntimeStateSubscriber interface {
	SubscribeRuntimeState(ctx context.Context) (<-chan domain.RuntimeActualStateSnapshot, error)
}

// RuntimeLogReader reads sanitized logs for a managed runtime target.
type RuntimeLogReader interface {
	ReadRouteLogs(ctx context.Context, routeDomain string, follow bool) (io.ReadCloser, error)
}

// RuntimeHealthClient reads runtime health without exposing adapter-specific inspect payloads.
type RuntimeHealthClient interface {
	PingRuntime(ctx context.Context) error
	RuntimeVersion(ctx context.Context) (string, error)
}

// RuntimeSelfUpdater sends policy-aware Gordon component self-update commands.
type RuntimeSelfUpdater interface {
	SelfUpdateRuntime(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error)
}

// RuntimeDrainAckReceiver receives runtime acknowledgements for edge drain requests.
type RuntimeDrainAckReceiver interface {
	AcknowledgeRuntimeDrain(ctx context.Context, routeDomain string, generation uint64) error
}
