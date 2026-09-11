package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// AppState persists desired/active app state, reservations, intents,
// operation journals, and ownership records. Implemented by the
// atomic-file filesystem adapter; consumed by the apps use case.
// All methods are safe for concurrent use within one process;
// cross-process coordination is owned by the implementation
// (store.lock flock). No secret values pass through this boundary.
type AppState interface {
	// Recover completes committed-but-unmaterialized apply intents
	// before any new mutation is accepted (recovery-before-mutation).
	Recover(ctx context.Context) error

	// LoadRecoveryInhibitions returns every durable recovery-inhibition
	// record of one app. Boot and periodic recovery must refuse to
	// start or restart an inhibited container ID.
	LoadRecoveryInhibitions(ctx context.Context, app string) ([]domain.AppRecoveryInhibition, error)

	// SaveRecoveryInhibition durably records one generation-scoped
	// recovery inhibition. Repeated calls for the same
	// (app, service, container ID) replace the existing record.
	SaveRecoveryInhibition(ctx context.Context, inhibition domain.AppRecoveryInhibition) error

	// ClearRecoveryInhibition drops the inhibition of one exact
	// generation. Clearing an absent record is not an error.
	ClearRecoveryInhibition(ctx context.Context, app, service, containerID string) error

	// LoadCheckpoint returns the global reservation checkpoint.
	LoadCheckpoint(ctx context.Context) (domain.AppStoreCheckpoint, error)

	// RegisterBackendBinds records Gordon-generated loopback backend
	// binds (owner gordon-backend) in the global checkpoint. It fails
	// closed on conflict with another app's desired/active/in-flight
	// claim. Old binds stay claimed until ReleaseBackendBinds after
	// verified withdrawal; same-app claims never self-conflict.
	RegisterBackendBinds(ctx context.Context, binds []domain.AppListenerReservation) error

	// ReleaseBackendBinds drops Gordon-generated loopback claims for one
	// retired container. Unknown claims are ignored.
	ReleaseBackendBinds(ctx context.Context, app, containerID string) error

	// ListApps returns normalized names of apps with any state.
	ListApps(ctx context.Context) ([]string, error)

	// LoadDesired returns the latest accepted desired revision.
	// ok is false when the app has no desired state.
	LoadDesired(ctx context.Context, app string) (domain.AppDesiredRevision, bool, error)

	// LoadRevision returns one immutable revision by id.
	LoadRevision(ctx context.Context, app, revision string) (domain.AppDesiredRevision, error)

	// ListRevisions returns revision ids ordered newest first.
	ListRevisions(ctx context.Context, app string) ([]string, error)

	// LoadActive returns the per-service effective state.
	// ok is false when the app was never deployed.
	LoadActive(ctx context.Context, app string) (domain.AppActive, bool, error)

	// LoadIntent returns the durable stopped/running intent.
	LoadIntent(ctx context.Context, app string) (domain.AppStopIntent, error)

	// SaveIntent persists running/stopped intent.
	SaveIntent(ctx context.Context, intent domain.AppStopIntent) error

	// LoadOwnership returns the ownership record (zero value when absent).
	LoadOwnership(ctx context.Context, app string) (domain.AppOwnership, error)

	// SaveOwnership persists the ownership record.
	SaveOwnership(ctx context.Context, ownership domain.AppOwnership) error

	// StageApply writes a staged intent containing the full candidate.
	// No reservation or revision is visible until CommitApply.
	StageApply(ctx context.Context, intent domain.AppApplyIntent) error

	// CommitApply is the single atomic commit point: staged→committed.
	CommitApply(ctx context.Context, app, intentID string) error

	// MaterializeApply finishes a committed intent: revision file,
	// desired pointer, reservation checkpoint update, intent→applied.
	// Idempotent by content hash; safe to replay after a crash.
	MaterializeApply(ctx context.Context, app, intentID string) error

	// LoadIntent returns one apply intent by id.
	LoadApplyIntent(ctx context.Context, app, intentID string) (domain.AppApplyIntent, error)

	// ListIntents returns apply intent ids for recovery scans.
	ListIntents(ctx context.Context, app string) ([]string, error)

	// CollectGarbage removes staged orphans, applied intents, and
	// unreferenced revisions beyond retention. Referenced revisions
	// (desired/active/in-flight) are never evicted.
	CollectGarbage(ctx context.Context, app string, inFlight []string) error

	// SaveOperation persists an operation journal record atomically.
	SaveOperation(ctx context.Context, op domain.AppOperation) error

	// LoadOperation returns one operation journal record.
	LoadOperation(ctx context.Context, app, opID string) (domain.AppOperation, error)

	// SaveActive persists the per-service effective state.
	SaveActive(ctx context.Context, active domain.AppActive) error

	// RetireApp atomically ends an app's incarnation: it archives the
	// live ownership record with every resource marked retained, drops
	// the live ownership record, resets the app UUID so a reapply
	// allocates a new incarnation, and clears desired, active, intent,
	// staged intents, and revisions. Retained resources stay protected
	// by the archived record and can never be adopted by a name reuse.
	RetireApp(ctx context.Context, app string) error
}

// AppStateReader is the ACTIVE/intent subset read paths need.
// Narrower than AppState so read paths never gain write access.
type AppStateReader interface {
	ListApps(ctx context.Context) ([]string, error)
	LoadActive(ctx context.Context, app string) (domain.AppActive, bool, error)
	LoadIntent(ctx context.Context, app string) (domain.AppStopIntent, error)
}

// AppTrafficRefresher is the app-facing traffic contract: one serialized
// HTTP/L4 publish boundary that supports both fail-closed withdrawal of
// one service and full publication of current verified state. Both
// operations read the latest validated config at call time and are
// serialized against reload and other rebuilds so a stale config can
// never win. WithdrawService must leave no forwarding for the named
// service once it returns nil; failing loudly is preferred to leaving
// stale L4 forwarding behind.
type AppTrafficRefresher interface {
	// RebuildTraffic re-projects verified ACTIVE state and applies the
	// full HTTP/L4 graph.
	RebuildTraffic(ctx context.Context) error
	// WithdrawService publishes the current graph without the named
	// app service, so a dead or unverified backend stops receiving
	// traffic. A nil return means stale forwarding is proven disabled.
	WithdrawService(ctx context.Context, app, service string) error
	// WithdrawServiceState drops the named service's recorded backend
	// binds in ACTIVE without applying the graph. It is the canonical
	// state-only withdrawal: ready-path failures use it when the
	// caller already owns publication (or none is due), so no second
	// routing-state write races the serialized WithdrawService path.
	WithdrawServiceState(ctx context.Context, app, service string) error
}
