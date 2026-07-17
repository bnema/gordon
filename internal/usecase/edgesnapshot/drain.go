package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

var (
	ErrDrainUnknown     = errors.New("unknown drain")
	ErrDrainStale       = errors.New("stale drain")
	ErrDrainUnexpected  = errors.New("unexpected edge drain")
	ErrDrainConflicting = errors.New("conflicting drain report")
)

// DrainState remains the edge transport shell. Domain owns the privacy and
// validation contract so every adapter uses the same drain identity.
type DrainState = domain.RouteDrainState

// DrainStateReceiver receives structurally valid drain reports. It is retained
// for simple consumers; control coordinators should use AuthenticatedDrainStateReceiver.
type DrainStateReceiver interface {
	ReportDrainState(context.Context, DrainState) error
}

// AuthenticatedDrainStateReceiver receives the identity derived by the RPC
// authentication interceptor. No edge-provided component identifier exists.
type AuthenticatedDrainStateReceiver interface {
	ReportAuthenticatedDrainState(context.Context, string, DrainState) error
}

// RuntimeDrainAckReceiver is the narrow opaque relay boundary. Runtime never
// learns an edge endpoint, container alias, or backing identity.
type RuntimeDrainAckReceiver interface {
	AcknowledgeRouteDrain(context.Context, domain.RouteDrainAck) error
}

type pendingDrain struct {
	state  domain.RouteDrainState
	status domain.RouteDrainStatus
}

// DrainCoordinatorOptions configures the first split milestone: exactly one
// expected edge, named gordon-edge unless control config chooses another name.
type DrainCoordinatorOptions struct {
	ExpectedEdgeComponentID string
	Now                     func() time.Time
	Runtime                 RuntimeDrainAckReceiver
}

// DrainCoordinator atomically observes control snapshot transitions and keeps a
// pending ledger for retired ready targets.
type DrainCoordinator struct {
	mu       sync.Mutex
	expected string
	now      func() time.Time
	runtime  RuntimeDrainAckReceiver
	pending  map[domain.RouteTargetKey]*pendingDrain
	seen     map[domain.RouteTargetKey]domain.RouteTargetGeneration
	stop     func()
}

// NewDrainCoordinator observes hub publications under the hub publication lock.
func NewDrainCoordinator(hub *SnapshotHub, options DrainCoordinatorOptions) (*DrainCoordinator, error) {
	if hub == nil {
		return nil, fmt.Errorf("snapshot hub is required")
	}
	expected := options.ExpectedEdgeComponentID
	if expected == "" {
		expected = "gordon-edge"
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	coordinator := &DrainCoordinator{
		expected: expected,
		now:      options.Now,
		runtime:  options.Runtime,
		pending:  make(map[domain.RouteTargetKey]*pendingDrain),
		seen:     make(map[domain.RouteTargetKey]domain.RouteTargetGeneration),
	}
	coordinator.stop = hub.ObserveTransitions(coordinator.observeTransition)
	return coordinator, nil
}

// Close stops observing future transitions.
func (c *DrainCoordinator) Close() {
	if c.stop != nil {
		c.stop()
		c.stop = nil
	}
}

func (c *DrainCoordinator) observeTransition(previous *domain.RouteTargetSnapshot, current domain.RouteTargetSnapshot) {
	if previous == nil {
		return
	}
	currentKeys := make(map[domain.RouteTargetKey]struct{}, len(current.Entries))
	for _, entry := range current.Entries {
		if entry.Ready() || entry.Draining() {
			currentKeys[entry.TargetKey] = struct{}{}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, old := range previous.Entries {
		if !old.Ready() || old.Generation >= current.Generation {
			continue
		}
		if _, stillCurrent := currentKeys[old.TargetKey]; stillCurrent {
			continue
		}
		if generation, exists := c.seen[old.TargetKey]; exists && generation >= current.Generation {
			continue
		}
		c.pending[old.TargetKey] = &pendingDrain{state: domain.RouteDrainState{
			CanonicalDomain:      old.CanonicalDomain,
			TransitionGeneration: current.Generation,
			OldTargetKey:         old.TargetKey,
		}, status: domain.RouteDrainStatusPending}
		c.seen[old.TargetKey] = current.Generation
	}
}

// ReportDrainState cannot establish an identity itself. RPC adapters must call
// ReportAuthenticatedDrainState after deriving it from authentication context.
func (c *DrainCoordinator) ReportDrainState(context.Context, DrainState) error {
	return ErrDrainUnexpected
}

// ReportAuthenticatedDrainState validates one expected edge report, records a
// server-observed acknowledgement time, and relays it to runtime at most once.
func (c *DrainCoordinator) ReportAuthenticatedDrainState(ctx context.Context, edgeComponentID string, state DrainState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if edgeComponentID != c.expected {
		return ErrDrainUnexpected
	}
	if err := state.Validate(); err != nil {
		return fmt.Errorf("invalid drain state: %w", err)
	}

	c.mu.Lock()
	pending, exists := c.pending[state.OldTargetKey]
	if !exists {
		if _, seen := c.seen[state.OldTargetKey]; seen {
			c.mu.Unlock()
			return ErrDrainStale
		}
		c.mu.Unlock()
		return ErrDrainUnknown
	}
	if pending.state.CanonicalDomain != state.CanonicalDomain || pending.state.TransitionGeneration != state.TransitionGeneration {
		c.mu.Unlock()
		return ErrDrainStale
	}
	if pending.status != domain.RouteDrainStatusPending {
		if sameDrainOutcome(pending.state, state) {
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		return ErrDrainConflicting
	}

	status := domain.RouteDrainStatusAcknowledged
	if state.TimeoutReason != domain.RouteDrainTimeoutReasonNone {
		status = domain.RouteDrainStatusTimedOut
	}
	// The edge's timestamp is informational only. Control records receipt time.
	state.AcknowledgedAt = c.now().UTC()
	ack := domain.RouteDrainAck{RouteDrainState: state, Status: status}
	pending.state, pending.status = state, status
	c.mu.Unlock()

	if c.runtime != nil {
		if err := c.runtime.AcknowledgeRouteDrain(ctx, ack); err != nil {
			// Preserve idempotency after a receiver success is uncertain: retrying a
			// report must not fan out duplicate runtime acknowledgements.
			return fmt.Errorf("relay route drain acknowledgement: %w", err)
		}
	}
	return nil
}

func sameDrainOutcome(previous, next domain.RouteDrainState) bool {
	return previous.CanonicalDomain == next.CanonicalDomain &&
		previous.TransitionGeneration == next.TransitionGeneration &&
		previous.OldTargetKey == next.OldTargetKey &&
		previous.InFlight == next.InFlight &&
		previous.TimeoutReason == next.TimeoutReason
}

// Timeout marks one pending drain as timed out with a control-owned reason and
// relays it once. Callers choose expiration from their configured drain policy.
func (c *DrainCoordinator) Timeout(ctx context.Context, key domain.RouteTargetKey) error {
	c.mu.Lock()
	pending, exists := c.pending[key]
	if !exists {
		c.mu.Unlock()
		return ErrDrainUnknown
	}
	if pending.status != domain.RouteDrainStatusPending {
		c.mu.Unlock()
		return nil
	}
	state := pending.state
	state.InFlight = 1 // Explicitly distinguish timeout from a clean zero drain.
	state.TimeoutReason = domain.RouteDrainTimeoutReasonControl
	state.AcknowledgedAt = c.now().UTC()
	pending.state, pending.status = state, domain.RouteDrainStatusTimedOut
	c.mu.Unlock()
	if c.runtime != nil {
		return c.runtime.AcknowledgeRouteDrain(ctx, domain.RouteDrainAck{RouteDrainState: state, Status: domain.RouteDrainStatusTimedOut})
	}
	return nil
}
