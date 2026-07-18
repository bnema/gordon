package out

import (
	"context"
	"io"

	"github.com/bnema/gordon/internal/domain"
)

// RuntimeCommandClient sends narrow runtime intent commands to a runtime worker.
// RuntimeCommandResultStore durably remembers terminal successful or denied
// command outcomes by their opaque dedupe key. Failed operations are never
// stored so callers may retry them.
type RuntimeCommandResultStore interface {
	LoadRuntimeCommandResult(ctx context.Context, dedupeKey string) (domain.RuntimeCommandResult, bool, error)
	SaveRuntimeCommandResult(ctx context.Context, dedupeKey string, result domain.RuntimeCommandResult) error
}

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

// RuntimeStateSubscriptionError classifies an initial runtime-state source
// failure without exposing transport details to control orchestration.
type RuntimeStateSubscriptionError struct {
	Retryable bool
	Err       error
}

func (e *RuntimeStateSubscriptionError) Error() string {
	return "runtime state subscription unavailable"
}

func (e *RuntimeStateSubscriptionError) Unwrap() error { return e.Err }

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

// RuntimeEnvironmentProbe exposes only migration-safe runtime facts. It never
// returns a socket address, daemon configuration, inspect payload, or errors
// suitable for disclosure to an admin API client.
type RuntimeEnvironmentProbe interface {
	ProbeRuntimeEnvironment(ctx context.Context) (RuntimeEnvironment, error)
}

// RuntimePublicListenerProbe answers one boolean for each requested public
// port. The runtime owns engine/socket inspection; callers receive neither
// process nor container identities.
type RuntimePublicListenerProbe interface {
	ProbePublicListeners(ctx context.Context, ports []int) ([]bool, error)
}

// RuntimeEnvironment is deliberately a small, sanitized preflight result.
type RuntimeEnvironment struct {
	Engine          string
	Rootless        bool
	APIReachable    bool
	ImageAvailable  bool
	ImagePullable   bool
	NetworkFeasible bool
	DiskAvailable   uint64
	DiskSufficient  bool
}

// RuntimeSelfUpdater sends policy-aware Gordon component self-update commands.
type RuntimeSelfUpdater interface {
	SelfUpdateRuntime(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error)
}

// RuntimeDrainAckReceiver receives runtime acknowledgements for edge drain requests.
type RuntimeDrainAckReceiver interface {
	AcknowledgeRuntimeDrain(ctx context.Context, routeDomain string, generation uint64, edgeComponentID string, targetAlias string) error
}

// RouteDrainAckReceiver receives a validated opaque drain acknowledgement.
// It supersedes RuntimeDrainAckReceiver for split edge deployments; the legacy
// alias-based method remains only for monolith compatibility.
type RouteDrainAckReceiver interface {
	AcknowledgeRouteDrain(ctx context.Context, acknowledgement domain.RouteDrainAck) error
}

// RouteDrainRegistrar records the canonical control transition before edge
// acknowledgement delivery, preventing a reused opaque key from accepting an
// acknowledgement from an earlier generation.
type RouteDrainRegistrar interface {
	PrepareRouteDrain(ctx context.Context, canonicalDomain string, generation domain.RouteTargetGeneration, oldTargetKey domain.RouteTargetKey) error
}

// RuntimeStandaloneServiceManager manages standalone services through narrow runtime commands and state.
type RuntimeStandaloneServiceManager interface {
	ApplyStandaloneService(ctx context.Context, command domain.ApplyStandaloneServiceCommand) (domain.RuntimeCommandResult, error)
	RemoveStandaloneService(ctx context.Context, command domain.RemoveStandaloneServiceCommand) (domain.RuntimeCommandResult, error)
	ListStandaloneServiceState(ctx context.Context) ([]domain.RuntimeStandaloneServiceState, error)
}

// RuntimeVolumeManager performs controlled volume operations through the runtime boundary.
type RuntimeVolumeManager interface {
	ListRuntimeVolumes(ctx context.Context) ([]*domain.VolumeInfo, error)
	RemoveRuntimeVolume(ctx context.Context, volumeName string, force bool) error
}

// RuntimeImageManager performs controlled image operations through the runtime boundary.
type RuntimeImageManager interface {
	ListRuntimeImages(ctx context.Context) ([]domain.RuntimeImageDetail, error)
	PruneRuntimeImages(ctx context.Context, danglingOnly bool) (domain.RuntimePruneResult, error)
}
