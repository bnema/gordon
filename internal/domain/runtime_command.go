package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(strconv.FormatUint(i.Generation, 10)))
	return hex.EncodeToString(h.Sum(nil))
}

// DeployRouteCommand asks the runtime to realize one managed route.
type DeployRouteCommand struct {
	RuntimeCommandIdentity
	Domain       string
	Image        string
	RouteVersion string
	Env          []string
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
	Domain string
	Reason string
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
}

// Validate checks ReconcileRuntimeCommand invariants.
func (c ReconcileRuntimeCommand) Validate() error {
	if err := c.RuntimeCommandIdentity.Validate(); err != nil {
		return err
	}
	if c.ExpectedRouteCount < 0 {
		return fmt.Errorf("%w: expected route count must be non-negative", ErrInvalidRuntimeCommand)
	}
	return nil
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
	return nil
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
