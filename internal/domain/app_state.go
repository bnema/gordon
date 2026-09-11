package domain

import "time"

// App state records are persistence shapes: the store adapter serializes them as
// JSON, the apps use case orchestrates transitions. No secret VALUES
// ever appear in these records — only secret paths.

// AppStoreVersion is the single supported app-state format version.
const AppStoreVersion = 1

// AppDefaultRevisionRetention is the default count of unreferenced
// revisions kept per app. Revisions referenced by desired state,
// active services, or unfinished operations are always protected,
// even beyond this limit. Configurable in main installation config.
const AppDefaultRevisionRetention = 8

// AppRevisionRetention is the compiled default retention, kept for
// callers that cannot reach a Store. Prefer Store.Retention, which
// honors the configured apps.revision_retention installation value.
//
// Deprecated: read the installation config or Store.Retention instead.
const AppRevisionRetention = AppDefaultRevisionRetention

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
	// AppResourceReleased marks a volume whose owning app explicitly
	// released it. Only this state makes a volume prune-eligible, and
	// only when the runtime labels agree with the durable record.
	AppResourceReleased = "released"
)

// OwnerGordonBackend marks Gordon-generated loopback backend binds in
// the global reservation checkpoint (plan D3: loopback-only,
// generated, reserved, distinguishable from public routes).
const OwnerGordonBackend = "gordon-backend"

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
	// ContainerID ties a Gordon-generated backend claim to the exact
	// container holding the bind, so old and replacement binds coexist
	// and release targets exactly one retired container.
	ContainerID string `json:"container_id,omitempty"`
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

// AppBackend is one resolved service backend endpoint: the loopback
// address plus the Gordon-published host port for one container port,
// tied to the exact container holding the bind. The proxy and readiness
// dial this endpoint rootless-first; a zero Backend means unbound
// (fail closed, never fall back to container IPs).
type AppBackend struct {
	// Host is the loopback address (127.0.0.1) when bound, empty when not.
	Host string `json:"host,omitempty"`
	// Port is the Gordon-published host port.
	Port int `json:"port,omitempty"`
	// ContainerPort is the container port this endpoint serves.
	ContainerPort int `json:"container_port,omitempty"`
	// ContainerID is the exact container holding the bind.
	ContainerID string `json:"container_id,omitempty"`
}

// Resolved reports whether the backend endpoint is dialable.
func (b AppBackend) Resolved() bool {
	return b.Host != "" && b.Port > 0 && b.ContainerID != ""
}

// BackendFor resolves one container port to its loopback endpoint from
// the recorded binds. ContainerPort is always set (declared manifest
// port); Host/Port/ContainerID resolve only when the bind is recorded
// (zero Backend otherwise: fail closed).
func (s AppEffectiveService) BackendFor(containerPort int) AppBackend {
	return s.BackendForProtocol(containerPort, NetworkProtocolTCP)
}

// BackendForProtocol resolves a protocol-specific container port bind.
func (s AppEffectiveService) BackendForProtocol(containerPort int, protocol NetworkProtocol) AppBackend {
	backend := AppBackend{ContainerPort: containerPort}
	binds := s.BackendBinds
	if protocol == NetworkProtocolUDP {
		binds = s.UDPBackendBinds
	}
	hostPort, ok := binds[containerPort]
	if !ok || hostPort <= 0 || s.Container == "" {
		return backend
	}
	backend.Host = "127.0.0.1"
	backend.Port = hostPort
	backend.ContainerID = s.Container
	return backend
}

type AppEffectiveService struct {
	EffectiveRevision string     `json:"effective_revision"`
	ActivatedBy       string     `json:"activated_by"`
	ActivatedAt       time.Time  `json:"activated_at"`
	Image             string     `json:"image"`
	Digest            string     `json:"digest,omitempty"`
	Container         string     `json:"container,omitempty"`
	Spec              AppService `json:"spec"`
	// BackendBinds maps container port -> 127.0.0.1 host port for the
	// Gordon-published loopback backends. Readiness and the proxy dial
	// these binds: they work rootless-first (no container-IP route
	// required) on Docker and Podman alike, and never expose backends
	// beyond loopback (04-network.md §4).
	BackendBinds    map[int]int `json:"backend_binds,omitempty"`
	UDPBackendBinds map[int]int `json:"udp_backend_binds,omitempty"`
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

// AppOperationStep is one durable journal step. Digest and Image are
// structured fields: a step that acquires or replaces an image records
// the pinned content digest and the runtime image reference here, so
// prune protection never has to parse human-readable Detail.
// AppOperationStep is one journaled step. Error carries a stable,
// log-free message; Diagnostics carries bounded application log output
// and is only returned to callers holding the logs read scope.
type AppOperationStep struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	// Digest pins the registry content digest this step resolved.
	Digest string `json:"digest,omitempty"`
	// Image is the runtime image reference this step acquired.
	Image string `json:"image,omitempty"`
	// Service names the app service this step applies to.
	Service string `json:"service,omitempty"`
	// Diagnostics is bounded, redacted failure output kept separate from
	// the stable Error. Retrieval requires the logs read scope.
	Diagnostics []string `json:"diagnostics,omitempty"`
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

// AppRecoveryInhibition is a durable, generation-scoped recovery
// inhibition record. It names one exact container ID that must never be
// started or restarted by boot or periodic recovery, because a
// replacement may already have written to a volume that this generation
// still owns. It carries no secret values. RestartUnsafe is a different
// fact (a successful volume deployment also sets it) and never implies
// inhibition.
type AppRecoveryInhibition struct {
	App         string    `json:"app"`
	AppID       string    `json:"app_id,omitempty"`
	Service     string    `json:"service"`
	ContainerID string    `json:"container_id"`
	Reason      string    `json:"reason"`
	Operation   string    `json:"operation,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// AppRecoveryInhibition reasons.
const (
	// AppInhibitReplacementPending marks the generation being replaced
	// while a volume-owning candidate may already be writing.
	AppInhibitReplacementPending = "replacement-pending"
)

// AppOwnership is the ownership record for one app.
// ID is the stable internal UUID, distinct from the public name.
// Remove frees the name but retains volumes/secrets under the old ID
// with their exact ownership records; a new app reusing the name
// never implicitly adopts them.
// AppOwnedImage tracks one app-pinned image (reference and digest only,
// never credentials).
//
//nolint:revive // Stutter matches the sibling AppOwned* record names.
type AppOwnedImage struct {
	Service   string `json:"service"`
	Reference string `json:"reference"`
	Digest    string `json:"digest,omitempty"`
	State     string `json:"state"`
}

type AppOwnership struct {
	App      string                        `json:"app"`
	ID       string                        `json:"id,omitempty"`
	Volumes  []AppOwnedVolume              `json:"volumes"`
	Services map[string]AppServiceRecovery `json:"services"`
	Secrets  []AppOwnedSecret              `json:"secrets"`
	Networks []AppOwnedNetwork             `json:"networks"`
	Images   []AppOwnedImage               `json:"images"`
}

// AppStoreCheckpoint is the global reservation checkpoint.
type AppStoreCheckpoint struct {
	Version      int                      `json:"version"`
	Reservations []AppListenerReservation `json:"reservations"`
}
