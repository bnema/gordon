package domain

import "time"

// App state records. Frozen by docs/plans/v2.50.0/02-state.md.
// These are persistence shapes: the store adapter serializes them as
// JSON, the apps use case orchestrates transitions. No secret VALUES
// ever appear in these records — only secret paths.

// AppStoreVersion is the single supported app-state format version.
const AppStoreVersion = 1

// AppRevisionRetention keeps the last N unreferenced revisions plus
// every revision referenced by desired/active/in-flight records.
const AppRevisionRetention = 32

// App intent states for the durable apply protocol.
const (
	AppIntentStaged    = "staged"
	AppIntentCommitted = "committed"
	AppIntentApplied   = "applied"
)

// App operation step states.
const (
	AppStepPending   = "pending"
	AppStepSucceeded = "succeeded"
	AppStepFailed    = "failed"
	AppStepNotRun    = "not-run"
)

// App operation outcomes over terminal per-service results.
const (
	AppOutcomeSuccess = "success"
	AppOutcomePartial = "partial"
	AppOutcomeFailed  = "failed"
)

// App owned-resource states.
const (
	AppResourceAttached = "attached"
	AppResourceRetained = "retained"
)

// AppListenerReservation is one global listener claim.
type AppListenerReservation struct {
	Proto   string `json:"proto"`
	IP      string `json:"ip,omitempty"`
	Port    int    `json:"port,omitempty"`
	Host    string `json:"host,omitempty"`
	Service string `json:"service"`
	App     string `json:"app"`
	Owner   string `json:"owner,omitempty"`
	Dual    *bool  `json:"dual,omitempty"`
}

// AppDesiredRevision is one accepted desired revision.
type AppDesiredRevision struct {
	Revision        string                   `json:"revision"`
	App             string                   `json:"app"`
	Supersedes      string                   `json:"supersedes,omitempty"`
	AcceptedAt      time.Time                `json:"accepted_at"`
	SourceSHA256    string                   `json:"source_sha256"`
	Spec            AppSpec                  `json:"spec"`
	Reservations    []AppListenerReservation `json:"reservations"`
	SecretsRequired []string                 `json:"secrets_required"`
	SecretsEnv      map[string]string        `json:"secrets_env"`
	Status          string                   `json:"status"`
}

// AppEffectiveService is one service's effective (activated) definition.
type AppEffectiveService struct {
	EffectiveRevision string     `json:"effective_revision"`
	ActivatedBy       string     `json:"activated_by"`
	ActivatedAt       time.Time  `json:"activated_at"`
	Image             string     `json:"image"`
	Digest            string     `json:"digest,omitempty"`
	Container         string     `json:"container,omitempty"`
	Spec              AppService `json:"spec"`
}

// AppActive is the per-service effective state of one app.
type AppActive struct {
	App               string                         `json:"app"`
	ConvergedRevision string                         `json:"converged_revision"`
	Converged         bool                           `json:"converged"`
	Services          map[string]AppEffectiveService `json:"services"`
	StopIntent        bool                           `json:"stop_intent"`
}

// AppStopIntent is the durable running/stopped intent.
type AppStopIntent struct {
	App       string    `json:"app"`
	Stopped   bool      `json:"stopped"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AppOperationStep is one durable journal step.
type AppOperationStep struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

// AppOperation is one durable operation journal record.
type AppOperation struct {
	Op            string             `json:"op"`
	Kind          string             `json:"kind"`
	App           string             `json:"app"`
	InputRevision string             `json:"input_revision"`
	StartedAt     time.Time          `json:"started_at"`
	Steps         []AppOperationStep `json:"steps"`
	Outcome       string             `json:"outcome,omitempty"`
}

// AppApplyIntent is one durable apply intent.
type AppApplyIntent struct {
	Intent       string                   `json:"intent"`
	App          string                   `json:"app"`
	State        string                   `json:"state"`
	Revision     string                   `json:"revision"`
	Supersedes   string                   `json:"supersedes,omitempty"`
	SourceSHA256 string                   `json:"source_sha256"`
	Spec         AppSpec                  `json:"spec"`
	Reservations []AppListenerReservation `json:"reservations"`
	CreatedAt    time.Time                `json:"created_at"`
}

// AppOwnedVolume tracks one app-owned volume.
type AppOwnedVolume struct {
	Name        string `json:"name"`
	Service     string `json:"service"`
	RuntimeName string `json:"runtime_name"`
	State       string `json:"state"`
}

// AppOwnedSecret tracks one app-referenced secret (path only, never value).
type AppOwnedSecret struct {
	Service string `json:"service"`
	Env     string `json:"env"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	State   string `json:"state"`
}

// AppOwnedNetwork tracks one app network.
type AppOwnedNetwork struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// AppServiceRecovery tracks per-service restart-safety flags.
type AppServiceRecovery struct {
	RestartUnsafe bool `json:"restart_unsafe"`
}

// AppOwnership is the ownership record for one app.
type AppOwnership struct {
	App      string                        `json:"app"`
	Volumes  []AppOwnedVolume              `json:"volumes"`
	Services map[string]AppServiceRecovery `json:"services"`
	Secrets  []AppOwnedSecret              `json:"secrets"`
	Networks []AppOwnedNetwork             `json:"networks"`
}

// AppStoreCheckpoint is the global reservation checkpoint.
type AppStoreCheckpoint struct {
	Version      int                      `json:"version"`
	Reservations []AppListenerReservation `json:"reservations"`
}
