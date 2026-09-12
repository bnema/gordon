package dto

import "time"

// App admin DTOs use stable wire-facing field names.
// No secret values exist in any shape here: secret references carry
// paths and env keys only. The versioned error envelope is a deliberate
// change from ErrorResponse for app mutations; clients accept both
// during the mixed-version window.

// AppError is the v2.50 app-mutation error envelope. Its stable fields are
// error/message/cause/hint.
// `logs` never appears on mutations. Clients accept the legacy
// single-field ErrorResponse during the mixed-version window.
type AppError struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Cause   string `json:"cause,omitempty"`
	Hint    string `json:"hint,omitempty"`
}

// AppDiffSection carries normalized added/removed/changed paths.
type AppDiffSection struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

// AppApplyRequest asks the daemon to validate and persist desired state.
type AppApplyRequest struct {
	ManifestTOML string `json:"manifest_toml"`
	DryRun       bool   `json:"dry_run,omitempty"`
}

// AppApplyResponse reports persistence success separately from deploy.
type AppApplyResponse struct {
	App               string         `json:"app"`
	FormerRevision    string         `json:"former_revision,omitempty"`
	ResultingRevision string         `json:"resulting_revision,omitempty"`
	Pending           bool           `json:"pending"`
	Noop              bool           `json:"noop,omitempty"`
	Diff              AppDiffSection `json:"diff"`
	Intent            string         `json:"intent,omitempty"`
}

// AppDeployRequest activates a captured revision.
type AppDeployRequest struct {
	Revision string `json:"revision,omitempty"`
	Service  string `json:"service,omitempty"`
}

// AppServiceResultDTO is one service's terminal deployment result.
type AppServiceResultDTO struct {
	Result            string `json:"result"`
	EffectiveRevision string `json:"effective_revision"`
	Before            string `json:"before,omitempty"`
	After             string `json:"after,omitempty"`
	RestartUnsafe     bool   `json:"restart_unsafe"`
	Error             string `json:"error,omitempty"`
}

// AppStepDTO is one journaled operation step.
type AppStepDTO struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Diagnostics is redacted failure output, populated only for callers
	// holding the logs read scope.
	Diagnostics []string `json:"diagnostics,omitempty"`
}

// AppCleanupWarningDTO records post-publication leftovers.
type AppCleanupWarningDTO struct {
	Service  string `json:"service"`
	Leftover string `json:"leftover"`
	Detail   string `json:"detail"`
}

// AppEffectiveDTO maps services to effective revisions.
type AppEffectiveDTO struct {
	Converged         bool              `json:"converged"`
	ConvergedRevision string            `json:"converged_revision,omitempty"`
	Services          map[string]string `json:"services"`
}

// AppRetainedDTO lists the resources an app owns and would retain on
// removal (names and paths, never values).
type AppRetainedDTO struct {
	Volumes []string `json:"volumes"`
	Secrets []string `json:"secrets"`
	Images  []string `json:"images"`
}

// AppDeployResponse reports deploy outcome with terminal results.
// Effective and Retained are filled from current app state when the app
// still exists; they are omitted when it does not.
type AppDeployResponse struct {
	Op              string                         `json:"op"`
	App             string                         `json:"app"`
	Revision        string                         `json:"revision"`
	Outcome         string                         `json:"outcome"`
	Services        map[string]AppServiceResultDTO `json:"services"`
	Steps           []AppStepDTO                   `json:"steps"`
	CleanupWarnings []AppCleanupWarningDTO         `json:"cleanup_warnings,omitempty"`
	Effective       *AppEffectiveDTO               `json:"effective,omitempty"`
	Retained        *AppRetainedDTO                `json:"retained,omitempty"`
}

// AppDesiredDTO summarizes desired state.
type AppDesiredDTO struct {
	Revision string `json:"revision,omitempty"`
	Status   string `json:"status,omitempty"`
	// Pending reports desired state ACTIVE has not reached yet.
	Pending bool `json:"pending"`
}

// AppActiveServiceDTO is one effective service for inspection.
type AppActiveServiceDTO struct {
	EffectiveRevision string `json:"effective_revision"`
	Digest            string `json:"digest,omitempty"`
	Container         string `json:"container,omitempty"`
	RestartUnsafe     bool   `json:"restart_unsafe"`
}

// AppActiveDTO carries per-service effective state.
type AppActiveDTO struct {
	Converged         bool                           `json:"converged"`
	ConvergedRevision string                         `json:"converged_revision,omitempty"`
	Services          map[string]AppActiveServiceDTO `json:"services"`
}

// AppIntentDTO carries durable stopped/running intent.
type AppIntentDTO struct {
	Stopped bool `json:"stopped"`
}

// AppLastOpDTO references the latest operation.
type AppLastOpDTO struct {
	Op        string    `json:"op"`
	Kind      string    `json:"kind,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
}

// AppShowResponse inspects desired + active + intent + ownership + op ref.
type AppShowResponse struct {
	App      string         `json:"app"`
	Desired  AppDesiredDTO  `json:"desired"`
	Active   AppActiveDTO   `json:"active"`
	Intent   AppIntentDTO   `json:"intent"`
	Retained AppRetainedDTO `json:"retained"`
	LastOp   *AppLastOpDTO  `json:"last_op,omitempty"`
}

// AppSummaryDTO is one row of the app list.
type AppSummaryDTO struct {
	App           string `json:"app"`
	Desired       string `json:"desired,omitempty"`
	DesiredStatus string `json:"desired_status,omitempty"`
	Active        string `json:"active,omitempty"`
	Converged     bool   `json:"converged"`
	Pending       bool   `json:"pending"`
	Stopped       bool   `json:"stopped"`
	LastOutcome   string `json:"last_outcome,omitempty"`
}

// AppDiffResponse reports normalized desired-vs-active diff.
type AppDiffResponse struct {
	App  string         `json:"app"`
	Diff AppDiffSection `json:"diff"`
}

// AppSecretSetRequest writes secret values (names pre-registered).
type AppSecretSetRequest struct {
	Service string            `json:"service"`
	Secrets map[string]string `json:"secrets"`
}

// AppSecretDeleteRequest removes one secret value.
type AppSecretDeleteRequest struct {
	Service string `json:"service"`
	Key     string `json:"key"`
}
