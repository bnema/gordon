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

	// LoadCheckpoint returns the global reservation checkpoint.
	LoadCheckpoint(ctx context.Context) (domain.AppStoreCheckpoint, error)

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
}
