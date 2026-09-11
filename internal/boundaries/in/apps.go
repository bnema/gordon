package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// AppService is the driving port for the v2.50 app lifecycle:
// apply + reads (list/show/diff), lifecycle verbs
// (deploy/stop/start/restart/remove), op recovery by idempotency key,
// and service-scoped secret value writes.
//
// The port owns its view structs and speaks domain types only — never
// adapter DTOs. Secret VALUES cross SetSecrets by necessity (that is the
// write path); every other method carries names, digests, and ids only.
//
// This is preparation: the interface is frozen here, unreachable.
// The admin handler and the service implementation wire in at cutover;
// until then no caller exists.
type AppService interface {
	// Apply validates a manifest spec and persists it as desired state,
	// or validates without persistence when dryRun is set. source is the
	// raw manifest bytes (hashed for audit, never re-read).
	Apply(ctx context.Context, spec domain.AppSpec, source []byte, dryRun bool) (*AppApplyResult, *AppDryRunResult, error)

	// List returns one summary per known app.
	List(ctx context.Context) ([]AppSummary, error)

	// Show inspects desired + active + intent + last op for one app.
	Show(ctx context.Context, app string) (*AppDetail, error)

	// Diff returns the normalized desired-vs-active diff for one app.
	Diff(ctx context.Context, app string) (domain.AppDiff, error)

	// Deploy activates a captured revision (empty revision means the
	// desired head; empty service means all services).
	Deploy(ctx context.Context, app, revision, service string) (*domain.AppOperation, error)

	// Stop persists the durable stopped intent and stops exact containers.
	Stop(ctx context.Context, app string) (*domain.AppOperation, error)

	// Start clears the stopped intent and ensures running from active.
	Start(ctx context.Context, app string) (*domain.AppOperation, error)

	// Restart restarts from pinned digests without re-resolution.
	// Empty service means all services.
	Restart(ctx context.Context, app, service string) (*domain.AppOperation, error)

	// Remove withdraws workloads; volumes and secrets are retained as
	// owned orphans and the name stays reserved.
	Remove(ctx context.Context, app string) (*domain.AppOperation, error)

	// OperationByKey recovers an ambiguous mutation outcome by the
	// client-supplied idempotency key.
	OperationByKey(ctx context.Context, app, key string) (*domain.AppOperation, error)

	// SetSecrets writes secret values for pre-registered names in one
	// service. service is required; every key must already exist in
	// desired or active state.
	SetSecrets(ctx context.Context, app, service string, values map[string]string) error

	// DeleteSecret removes one secret value. Refused when referenced.
	DeleteSecret(ctx context.Context, app, service, key string) error
}

// AppApplyResult describes one accepted (or no-op) apply.
type AppApplyResult struct {
	App               string
	FormerRevision    string
	ResultingRevision string
	Noop              bool
	Pending           bool
	Diff              domain.AppDiff
	IntentID          string
}

// AppDryRunResult describes validation without persistence.
// The resulting revision is a preview only and is never stored.
type AppDryRunResult struct {
	App   string
	Valid bool
	Diff  domain.AppDiff
}

// AppSummary is one row of the app list: names and revisions only.
type AppSummary struct {
	App       string
	Desired   string
	Active    string
	Converged bool
	Stopped   bool
}

// AppServiceView is one service's effective state for inspection.
// Digest and container are ids, never secret values.
type AppServiceView struct {
	EffectiveRevision string
	Digest            string
	Container         string
	RestartUnsafe     bool
}

// AppDetail inspects desired + active + intent + last op for one app.
type AppDetail struct {
	App               string
	DesiredRevision   string
	DesiredStatus     string
	Converged         bool
	ConvergedRevision string
	Services          map[string]AppServiceView
	Stopped           bool
	LastOp            string
	LastOutcome       string
}
