package in

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// AppService is the driving port for the v3 app lifecycle:
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
	// or validates without persistence when DryRun is set. A non-dry-run
	// apply that would replace images introduced by set-image is refused
	// with a *domain.SetImageRevertError (domain.ErrAppSetImageRevert)
	// unless AllowSetImageRevert is set.
	Apply(ctx context.Context, input AppApplyInput) (*AppApplyResult, *AppDryRunResult, error)

	// List returns one summary per known app.
	List(ctx context.Context) ([]AppSummary, error)

	// Show inspects desired + active + intent + last op for one app.
	Show(ctx context.Context, app string) (*AppDetail, error)

	// Diff returns the normalized desired-vs-active diff for one app.
	Diff(ctx context.Context, app string) (domain.AppDiff, error)

	// Deploy activates a captured revision (empty revision means the
	// desired head). An empty service targets every service, which a
	// multi-service app accepts only with all set (domain.ErrAppServiceScope).
	Deploy(ctx context.Context, app, revision, service string, all bool, idempotencyKey string) (*domain.AppOperation, error)

	// Stop persists the durable stopped intent and stops exact containers.
	Stop(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error)

	// Start clears the stopped intent and ensures running from active.
	Start(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error)

	// Restart restarts from pinned digests without re-resolution.
	// An empty service targets every service, which a multi-service app
	// accepts only with all set (domain.ErrAppServiceScope).
	Restart(ctx context.Context, app, service string, all bool, idempotencyKey string) (*domain.AppOperation, error)

	// Remove withdraws workloads; volumes and secrets are retained as
	// owned orphans and the name stays reserved.
	Remove(ctx context.Context, app, idempotencyKey string) (*domain.AppOperation, error)

	// SetImage runs exactly one registry digest in one service: it accepts a
	// desired revision whose only change is that service image and starts
	// its deploy, shaped like Deploy (a running journal for a new claim, the
	// stored journal for a replay of the same key).
	SetImage(ctx context.Context, input SetImageInput) (*domain.AppOperation, error)

	// OperationByKey recovers an ambiguous mutation outcome by the
	// client-supplied idempotency key.
	OperationByKey(ctx context.Context, app, key string) (*domain.AppOperation, error)

	// PrincipalOperationByKey is OperationByKey scoped to one principal: an
	// operation recorded for another principal (or none) reports
	// domain.ErrAppOperationNotFound, so a caller never learns about keys it
	// did not submit.
	PrincipalOperationByKey(ctx context.Context, app, key string, principal domain.WorkloadPrincipal) (*domain.AppOperation, error)

	// ListSecrets returns metadata-only desired and active registrations.
	ListSecrets(ctx context.Context, app, service string) ([]AppSecretMetadata, error)

	// SetSecrets writes secret values for pre-registered names in one
	// service. service is required; every key must already exist in
	// desired or active state.
	SetSecrets(ctx context.Context, app, service string, values map[string]string) error

	// DeleteSecret removes one secret value. Refused when referenced.
	DeleteSecret(ctx context.Context, app, service, key string) error
}

// SetImageInput is one set-image request. Principal identifies the caller;
// LocalOwner is set only for the owner-only admin socket, which may set the image
// without a set-image policy.
type SetImageInput struct {
	App              string
	Service          string
	Image            string
	ExpectedRevision string
	IdempotencyKey   string
	Principal        domain.WorkloadPrincipal
	LocalOwner       bool
}

// AppApplyInput is one apply request. Source is the raw manifest bytes
// (hashed for audit, never re-read). ExpectedRevision, when set, must equal
// the current desired revision (domain.ErrAppStateConflict otherwise).
type AppApplyInput struct {
	Spec                domain.AppSpec
	Source              []byte
	DryRun              bool
	AllowSetImageRevert bool
	ExpectedRevision    string
}

// AppSecretMetadata is a secret registration without its value.
type AppSecretMetadata struct {
	Service  string
	Key      string
	Name     string
	Source   string
	Presence string
}

// AppApplyResult describes one accepted (or no-op) apply.
// SetImageReverts lists the set-image changes the apply replaced.
type AppApplyResult struct {
	App               string
	FormerRevision    string
	ResultingRevision string
	Noop              bool
	Pending           bool
	Diff              domain.AppDiff
	IntentID          string
	SetImageReverts   []domain.AppSetImageRevert
}

// AppDryRunResult describes validation without persistence.
// The resulting revision is a preview only and is never stored.
// SetImageReverts lists the set-image changes a real apply would replace.
type AppDryRunResult struct {
	App             string
	Valid           bool
	Diff            domain.AppDiff
	SetImageReverts []domain.AppSetImageRevert
}

// AppSummary is one row of the app list: revisions, acceptance status,
// and the latest recorded outcome.
type AppSummary struct {
	App           string
	Desired       string
	DesiredStatus string
	Active        string
	Converged     bool
	// Pending reports desired state that the active revision has not
	// reached yet. It is derived from desired vs ACTIVE, never from a
	// journal.
	Pending     bool
	Stopped     bool
	LastOutcome string
}

// AppServiceView is one service's effective state for inspection.
// Digest and container are ids, never secret values.
type AppServiceView struct {
	EffectiveRevision string
	Digest            string
	Container         string
	RestartUnsafe     bool
}

// AppRetainedView lists the resources the app owns and would retain on
// removal: runtime volume names, secret paths, and pinned image
// references. It never carries secret values.
type AppRetainedView struct {
	Volumes []string
	Secrets []string
	Images  []string
}

// AppDetail inspects desired + active + intent + ownership + last op for
// one app.
type AppDetail struct {
	App               string
	DesiredRevision   string
	DesiredStatus     string
	Pending           bool
	Converged         bool
	ConvergedRevision string
	Services          map[string]AppServiceView
	Stopped           bool
	Retained          AppRetainedView
	LastOp            string
	LastOpKind        string
	LastOutcome       string
	LastOpStartedAt   time.Time
}
