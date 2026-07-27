package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RuntimeCommandID identifies one runtime command envelope.
type RuntimeCommandID string

// RuntimeCommandStatus describes the terminal or in-flight status of a runtime command.
type RuntimeCommandStatus string

const (
	RuntimeCommandStatusPending   RuntimeCommandStatus = "pending"
	RuntimeCommandStatusRunning   RuntimeCommandStatus = "running"
	RuntimeCommandStatusSucceeded RuntimeCommandStatus = "succeeded"
	RuntimeCommandStatusFailed    RuntimeCommandStatus = "failed"
	RuntimeCommandStatusDenied    RuntimeCommandStatus = "denied"
)

// RuntimeSelfUpdatePolicy identifies how Gordon component self-updates are authorized.
type RuntimeSelfUpdatePolicy string

const (
	RuntimeSelfUpdatePolicyManualApproval RuntimeSelfUpdatePolicy = "manual_approval"
	RuntimeSelfUpdatePolicyPinnedVersion  RuntimeSelfUpdatePolicy = "pinned_version"
	RuntimeSelfUpdatePolicyAutomaticPatch RuntimeSelfUpdatePolicy = "automatic_patch"
)

// RuntimeComponentLifecycleAction is the explicit, allowlisted lifecycle
// vocabulary available to migration. It is intentionally not a raw runtime
// operation or arbitrary Podman command.
type RuntimeComponentLifecycleAction string

const (
	RuntimeComponentLifecycleReplace         RuntimeComponentLifecycleAction = "replace"
	RuntimeComponentLifecycleEnsureNetwork   RuntimeComponentLifecycleAction = "ensure_network"
	RuntimeComponentLifecycleStart           RuntimeComponentLifecycleAction = "start"
	RuntimeComponentLifecycleStop            RuntimeComponentLifecycleAction = "stop"
	RuntimeComponentLifecycleHealth          RuntimeComponentLifecycleAction = "health"
	RuntimeComponentLifecycleLogs            RuntimeComponentLifecycleAction = "logs"
	RuntimeComponentLifecycleConnect         RuntimeComponentLifecycleAction = "connect"
	RuntimeComponentLifecycleRemove          RuntimeComponentLifecycleAction = "remove"
	RuntimeComponentLifecycleTransferChannel RuntimeComponentLifecycleAction = "transfer_channel"
	// Activate exposes the prepared edge generation only after the control
	// plane's switch prerequisites are satisfied.
	RuntimeComponentLifecycleActivate RuntimeComponentLifecycleAction = "activate"
	// Drain keeps the previous edge generation usable while it finishes work.
	RuntimeComponentLifecycleDrain RuntimeComponentLifecycleAction = "drain"
)

var (
	// ErrInvalidRuntimeCommand is returned when a runtime intent command is malformed.
	ErrInvalidRuntimeCommand = errors.New("invalid runtime command")
)

// RuntimeCommandIdentity is embedded by concrete runtime intent commands.
type RuntimeCommandIdentity struct {
	ID                RuntimeCommandID
	IdempotencyKey    string
	Generation        uint64
	SourceComponentID string
	RequestedAt       time.Time
}

// Validate checks command identity fields required for idempotent runtime intents.
func (i RuntimeCommandIdentity) Validate() error {
	if strings.TrimSpace(string(i.ID)) == "" {
		return fmt.Errorf("%w: id is required", ErrInvalidRuntimeCommand)
	}
	if strings.TrimSpace(i.IdempotencyKey) == "" {
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidRuntimeCommand)
	}
	if i.Generation == 0 {
		return fmt.Errorf("%w: generation is required", ErrInvalidRuntimeCommand)
	}
	if strings.TrimSpace(i.SourceComponentID) == "" {
		return fmt.Errorf("%w: source component id is required", ErrInvalidRuntimeCommand)
	}
	return nil
}

// DedupeKey returns a stable idempotency key for runtime command processing.
func (i RuntimeCommandIdentity) DedupeKey(kind string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(strings.TrimSpace(i.IdempotencyKey)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(kind))
	return hex.EncodeToString(h.Sum(nil))
}

// DeployRouteCommand asks the runtime to realize one managed route.
type DeployRouteCommand struct {
	RuntimeCommandIdentity
	Domain         string
	Image          string
	RouteVersion   string
	Env            []string
	InternalDeploy bool
}

// Validate checks DeployRouteCommand invariants.
func (c DeployRouteCommand) Validate() error {
	if err := c.RuntimeCommandIdentity.Validate(); err != nil {
		return err
	}
	if !IsValidRouteDomain(c.Domain) {
		return fmt.Errorf("%w: route domain is invalid", ErrInvalidRuntimeCommand)
	}
	if strings.TrimSpace(c.Image) == "" {
		return fmt.Errorf("%w: image is required", ErrInvalidRuntimeCommand)
	}
	return nil
}

// RestartRouteCommand asks the runtime to restart the container backing one route.
type RestartRouteCommand struct {
	RuntimeCommandIdentity
	Domain          string
	Reason          string
	WithAttachments bool
}

// Validate checks RestartRouteCommand invariants.
func (c RestartRouteCommand) Validate() error {
	if err := c.RuntimeCommandIdentity.Validate(); err != nil {
		return err
	}
	if !IsValidRouteDomain(c.Domain) {
		return fmt.Errorf("%w: route domain is invalid", ErrInvalidRuntimeCommand)
	}
	return nil
}

// RemoveRouteCommand asks the runtime to remove runtime artifacts for one route.
type RemoveRouteCommand struct {
	RuntimeCommandIdentity
	Domain string
	Force  bool
}

// Validate checks RemoveRouteCommand invariants.
func (c RemoveRouteCommand) Validate() error {
	if err := c.RuntimeCommandIdentity.Validate(); err != nil {
		return err
	}
	if !IsValidRouteDomain(c.Domain) {
		return fmt.Errorf("%w: route domain is invalid", ErrInvalidRuntimeCommand)
	}
	return nil
}

// ReconcileRuntimeCommand asks the runtime to compare desired and actual runtime state.
type ReconcileRuntimeCommand struct {
	RuntimeCommandIdentity
	Reason              string
	ExpectedRouteCount  int
	DesiredStateVersion string
	DesiredRoutes       []Route
}

// Validate checks ReconcileRuntimeCommand invariants.
func (c ReconcileRuntimeCommand) Validate() error {
	if err := c.RuntimeCommandIdentity.Validate(); err != nil {
		return err
	}
	if c.ExpectedRouteCount < 0 {
		return fmt.Errorf("%w: expected route count must be non-negative", ErrInvalidRuntimeCommand)
	}
	if c.ExpectedRouteCount != len(c.DesiredRoutes) {
		return fmt.Errorf("%w: desired routes count does not match expected route count", ErrInvalidRuntimeCommand)
	}
	for _, route := range c.DesiredRoutes {
		if !IsValidRouteDomain(route.Domain) {
			return fmt.Errorf("%w: desired route domain is invalid", ErrInvalidRuntimeCommand)
		}
		if strings.TrimSpace(route.Image) == "" {
			return fmt.Errorf("%w: desired route image is required", ErrInvalidRuntimeCommand)
		}
	}
	return nil
}

const (
	// MaxEdgeAppNetworks bounds a cross-component activation request so it
	// cannot be used to turn the runtime into a general network attachment API.
	MaxEdgeAppNetworks = 16
	// MaxEdgeAppNetworkNameLength admits Docker/Podman-compatible managed names
	// while limiting untrusted RPC allocation and log payloads.
	MaxEdgeAppNetworkNameLength = 255
)

// RuntimeComponentLifecycleProfile is the exact process and rootless Podman
// mount contract for one split Gordon role. Authenticated lifecycle commands
// carry this profile so mutation and read-only actions cannot drift apart.
type RuntimeComponentLifecycleProfile struct {
	ProcessIdentity         ComponentProcessIdentity
	UsernsMode              string
	CapDrop                 []string
	NoNewPrivileges         bool
	GenerationVolumeOptions []string
}

// FixedRuntimeComponentLifecycleProfile returns the immutable runtime profile
// for a split role. Edge is stateless and therefore has no generation-volume
// ownership option.
func FixedRuntimeComponentLifecycleProfile(role ComponentRole) (RuntimeComponentLifecycleProfile, bool) {
	identity, ok := FixedComponentProcessIdentity(role)
	if !ok {
		return RuntimeComponentLifecycleProfile{}, false
	}
	profile := RuntimeComponentLifecycleProfile{
		ProcessIdentity: identity,
		UsernsMode:      "keep-id:uid=" + strconv.Itoa(identity.UID) + ",gid=" + strconv.Itoa(identity.GID),
		CapDrop:         []string{"ALL"},
		NoNewPrivileges: true,
	}
	if role != ComponentRoleEdge {
		profile.GenerationVolumeOptions = []string{ContainerVolumeOptionChown}
	}
	return profile, true
}

// IsFixedFor reports whether the complete profile is the immutable contract
// for role. Nil and empty slices compare equally only where the fixed profile
// itself is empty (the stateless edge volume profile).
func (p RuntimeComponentLifecycleProfile) IsFixedFor(role ComponentRole) bool {
	expected, ok := FixedRuntimeComponentLifecycleProfile(role)
	return ok && p.ProcessIdentity == expected.ProcessIdentity && p.UsernsMode == expected.UsernsMode &&
		slices.Equal(p.CapDrop, expected.CapDrop) && p.NoNewPrivileges == expected.NoNewPrivileges &&
		slices.Equal(p.GenerationVolumeOptions, expected.GenerationVolumeOptions)
}

// RuntimeSelfUpdateCommand asks a managed Gordon runtime component to update itself under policy.
type RuntimeSelfUpdateCommand struct {
	RuntimeCommandIdentity
	TargetComponentID   string
	TargetComponentRole ComponentRole
	CurrentVersion      string
	TargetVersion       string
	Policy              RuntimeSelfUpdatePolicy
	PolicyDecisionID    string
	ApprovedBy          string
	// Lifecycle fields are a stable desired identity contract for Gordon
	// components. They never contain a socket path, secret value, or raw engine
	// option. Empty action retains pre-split self-update compatibility.
	LifecycleAction  RuntimeComponentLifecycleAction
	LifecycleProfile RuntimeComponentLifecycleProfile
	DesiredImage     string
	DesiredStateHash string
	InternalNetwork  string
	EnvironmentFile  string
	// ConfigFile is an approved, read-only role manifest under the migration
	// directory. It is not a general host bind or a secret transport.
	ConfigFile string
	// PortPublishes are explicit checkpointed listener bindings. Lifecycle
	// policy validates their role, address, and phase; raw engine port options
	// are never accepted from control.
	PortPublishes []ContainerPortPublish
	// OldServingComponentID and FinalPortPublishes are accepted only for edge
	// activation. The runtime verifies that the old target is an existing
	// Gordon-managed container and that final host ports exactly match it.
	// They are not arbitrary runtime targets or engine options.
	OldServingComponentID string
	FinalPortPublishes    []ContainerPortPublish
	// EdgeAppNetworks is the exact set of managed application networks that a
	// replacement edge must retain during activation. It is accepted only by
	// the runtime-owned edge activation transaction, which verifies both the
	// managed-network policy and the prepared edge attachment before use.
	EdgeAppNetworks []string
	PreserveVolumes bool
}

// Validate checks that self-update is a Gordon component lifecycle operation, not an unmanaged mutation.
func (c RuntimeSelfUpdateCommand) Validate() error {
	if err := c.RuntimeCommandIdentity.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(c.TargetComponentID) == "" {
		return fmt.Errorf("%w: target component id is required", ErrInvalidRuntimeCommand)
	}
	if !IsKnownComponentRole(c.TargetComponentRole) {
		return fmt.Errorf("%w: self-update target component role is invalid", ErrInvalidRuntimeCommand)
	}
	if strings.TrimSpace(c.TargetVersion) == "" {
		return fmt.Errorf("%w: target version is required", ErrInvalidRuntimeCommand)
	}
	if !isKnownRuntimeSelfUpdatePolicy(c.Policy) {
		return fmt.Errorf("%w: self-update policy is invalid", ErrInvalidRuntimeCommand)
	}
	if strings.TrimSpace(c.PolicyDecisionID) == "" {
		return fmt.Errorf("%w: policy decision id is required", ErrInvalidRuntimeCommand)
	}
	if c.LifecycleAction != "" && !isKnownRuntimeComponentLifecycleAction(c.LifecycleAction) {
		return fmt.Errorf("%w: component lifecycle action is invalid", ErrInvalidRuntimeCommand)
	}
	if c.LifecycleAction != "" && c.LifecycleAction != RuntimeComponentLifecycleEnsureNetwork && !c.LifecycleProfile.IsFixedFor(c.TargetComponentRole) {
		return fmt.Errorf("%w: component lifecycle profile is invalid", ErrInvalidRuntimeCommand)
	}
	if err := validateEdgeAppNetworks(c); err != nil {
		return err
	}
	return nil
}

func validateEdgeAppNetworks(command RuntimeSelfUpdateCommand) error {
	if len(command.EdgeAppNetworks) == 0 {
		return nil
	}
	if command.TargetComponentRole != ComponentRoleEdge || command.LifecycleAction != RuntimeComponentLifecycleActivate {
		return fmt.Errorf("%w: edge app networks require edge activation", ErrInvalidRuntimeCommand)
	}
	if len(command.EdgeAppNetworks) > MaxEdgeAppNetworks {
		return fmt.Errorf("%w: too many edge app networks", ErrInvalidRuntimeCommand)
	}
	seen := make(map[string]struct{}, len(command.EdgeAppNetworks))
	for _, name := range command.EdgeAppNetworks {
		if !IsSafeEdgeAppNetworkName(name) {
			return fmt.Errorf("%w: edge app network name is invalid", ErrInvalidRuntimeCommand)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: duplicate edge app network", ErrInvalidRuntimeCommand)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// IsSafeEdgeAppNetworkName accepts a single portable network name without
// normalizing it. Validation is deliberately fail-closed so its raw input can
// never be interpreted as an engine ID, path, or shell fragment downstream.
func IsSafeEdgeAppNetworkName(name string) bool {
	if name == "" || name != strings.TrimSpace(name) || len(name) > MaxEdgeAppNetworkNameLength || !isASCIIAlphanumeric(name[0]) {
		return false
	}
	for index := range len(name) {
		if !isSafeNetworkNameCharacter(name[index]) {
			return false
		}
	}
	return true
}

func isSafeNetworkNameCharacter(char byte) bool {
	return isASCIIAlphanumeric(char) || char == '.' || char == '_' || char == '-'
}

func isASCIIAlphanumeric(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}

// RuntimeCommandError is a sanitized command failure suitable for cross-boundary transport.
type RuntimeCommandError struct {
	Code      string
	Message   string
	Retryable bool
}

// RuntimeCommandResult records the sanitized outcome of a runtime command.
type RuntimeCommandResult struct {
	CommandID      RuntimeCommandID
	IdempotencyKey string
	Generation     uint64
	Status         RuntimeCommandStatus
	StartedAt      time.Time
	CompletedAt    time.Time
	Error          *RuntimeCommandError
}

func isKnownRuntimeComponentLifecycleAction(action RuntimeComponentLifecycleAction) bool {
	switch action {
	case RuntimeComponentLifecycleReplace, RuntimeComponentLifecycleEnsureNetwork,
		RuntimeComponentLifecycleStart, RuntimeComponentLifecycleStop,
		RuntimeComponentLifecycleHealth, RuntimeComponentLifecycleLogs,
		RuntimeComponentLifecycleConnect, RuntimeComponentLifecycleRemove,
		RuntimeComponentLifecycleTransferChannel, RuntimeComponentLifecycleActivate,
		RuntimeComponentLifecycleDrain:
		return true
	default:
		return false
	}
}

func isKnownRuntimeSelfUpdatePolicy(policy RuntimeSelfUpdatePolicy) bool {
	switch policy {
	case RuntimeSelfUpdatePolicyManualApproval,
		RuntimeSelfUpdatePolicyPinnedVersion,
		RuntimeSelfUpdatePolicyAutomaticPatch:
		return true
	default:
		return false
	}
}
