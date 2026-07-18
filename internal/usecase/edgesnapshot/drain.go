package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const maxDrainLedgerEntries = 1024

var (
	ErrDrainUnknown     = errors.New("unknown drain")
	ErrDrainStale       = errors.New("stale drain")
	ErrDrainUnexpected  = errors.New("unexpected edge drain")
	ErrDrainConflicting = errors.New("conflicting drain report")
)

// DrainState remains the edge transport shell. Domain owns the privacy and
// validation contract so every adapter uses the same drain identity.
type DrainState = domain.RouteDrainState

type DrainStateReceiver interface {
	ReportDrainState(context.Context, DrainState) error
}
type AuthenticatedDrainStateReceiver interface {
	ReportAuthenticatedDrainState(context.Context, string, DrainState) error
}
type RuntimeDrainAckReceiver interface {
	AcknowledgeRouteDrain(context.Context, domain.RouteDrainAck) error
}

// RuntimeDrainRegistrar pins the exact control transition at runtime before an
// edge can acknowledge it. It intentionally contains only opaque route data.
type RuntimeDrainRegistrar interface {
	PrepareRouteDrain(context.Context, string, domain.RouteTargetGeneration, domain.RouteTargetKey) error
}

type pendingDrain struct {
	state     domain.RouteDrainState
	status    domain.RouteDrainStatus
	observed  bool
	ack       domain.RouteDrainAck
	relaying  bool
	relayDone chan struct{}
}

type DrainCoordinatorOptions struct {
	ExpectedEdgeComponentID string
	Now                     func() time.Time
	Runtime                 RuntimeDrainAckReceiver
}

// DrainCoordinator records edge observation separately from runtime delivery:
// a drain is terminal only after runtime has accepted its relay.
type DrainCoordinator struct {
	mu             sync.Mutex
	expected       string
	now            func() time.Time
	runtime        RuntimeDrainAckReceiver
	pending        map[domain.RouteTargetKey]*pendingDrain
	completed      map[domain.RouteTargetKey]*pendingDrain
	completedOrder []domain.RouteTargetKey
	seen           map[domain.RouteTargetKey]domain.RouteTargetGeneration
	stop           func()
}

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
	coordinator := &DrainCoordinator{expected: expected, now: options.Now, runtime: options.Runtime, pending: make(map[domain.RouteTargetKey]*pendingDrain), completed: make(map[domain.RouteTargetKey]*pendingDrain), seen: make(map[domain.RouteTargetKey]domain.RouteTargetGeneration)}
	coordinator.stop = hub.ObserveTransitions(coordinator.observeTransition)
	return coordinator, nil
}

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
	for _, old := range previous.Entries {
		if !old.Ready() || old.Generation >= current.Generation {
			continue
		}
		if _, stillCurrent := currentKeys[old.TargetKey]; stillCurrent {
			continue
		}
		c.mu.Lock()
		_, exists := c.seen[old.TargetKey]
		c.mu.Unlock()
		if exists {
			continue
		}

		// Register first. A failed registration means runtime will use its local
		// drain timeout rather than accepting an unbound acknowledgement.
		if registrar, ok := c.runtime.(RuntimeDrainRegistrar); ok {
			if err := registrar.PrepareRouteDrain(context.Background(), old.CanonicalDomain, current.Generation, old.TargetKey); err != nil {
				continue
			}
		}
		c.mu.Lock()
		if _, exists := c.seen[old.TargetKey]; !exists && len(c.pending) < maxDrainLedgerEntries {
			c.pending[old.TargetKey] = &pendingDrain{state: domain.RouteDrainState{CanonicalDomain: old.CanonicalDomain, TransitionGeneration: current.Generation, OldTargetKey: old.TargetKey}, status: domain.RouteDrainStatusPending}
			c.seen[old.TargetKey] = current.Generation
		}
		c.mu.Unlock()
	}
}

func (c *DrainCoordinator) ReportDrainState(context.Context, DrainState) error {
	return ErrDrainUnexpected
}

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
	return c.relay(ctx, state)
}

// relay is deliberately a small singleflight state machine. A failed relay
// leaves the edge observation intact but non-terminal, so the identical edge
// retry reaches runtime again. Concurrent duplicates wait for the in-flight
// relay and observe its result rather than racing terminal state.
//
//nolint:gocyclo // Relay admission, duplicate waiting, and terminal commit share one mutex-protected state machine.
func (c *DrainCoordinator) relay(ctx context.Context, report domain.RouteDrainState) error {
	for {
		c.mu.Lock()
		pending, exists := c.pending[report.OldTargetKey]
		if !exists {
			if completed := c.completed[report.OldTargetKey]; completed != nil {
				if sameDrainOutcome(completed.state, report) {
					c.mu.Unlock()
					return nil
				}
				c.mu.Unlock()
				return ErrDrainConflicting
			}
			if _, seen := c.seen[report.OldTargetKey]; seen {
				c.mu.Unlock()
				return ErrDrainStale
			}
			c.mu.Unlock()
			return ErrDrainUnknown
		}
		if pending.state.CanonicalDomain != report.CanonicalDomain || pending.state.TransitionGeneration != report.TransitionGeneration {
			c.mu.Unlock()
			return ErrDrainStale
		}
		if pending.observed && !sameDrainOutcome(pending.ack.RouteDrainState, report) {
			c.mu.Unlock()
			return ErrDrainConflicting
		}
		if pending.status != domain.RouteDrainStatusPending {
			c.mu.Unlock()
			return nil
		}
		if pending.relaying {
			done := pending.relayDone
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
				continue
			}
		}
		if !pending.observed {
			report.AcknowledgedAt = c.now().UTC()
			status := domain.RouteDrainStatusAcknowledged
			if report.TimeoutReason != domain.RouteDrainTimeoutReasonNone {
				status = domain.RouteDrainStatusTimedOut
			}
			pending.observed = true
			pending.ack = domain.RouteDrainAck{RouteDrainState: report, Status: status}
		}
		ack := pending.ack
		pending.relaying, pending.relayDone = true, make(chan struct{})
		done := pending.relayDone
		c.mu.Unlock()

		var err error
		if c.runtime != nil {
			err = c.runtime.AcknowledgeRouteDrain(ctx, ack)
		}
		c.mu.Lock()
		pending.relaying = false
		close(done)
		if err == nil {
			pending.status, pending.state = ack.Status, ack.RouteDrainState
			delete(c.pending, report.OldTargetKey)
			c.completed[report.OldTargetKey] = pending
			c.completedOrder = append(c.completedOrder, report.OldTargetKey)
			for len(c.completedOrder) > maxDrainLedgerEntries {
				old := c.completedOrder[0]
				c.completedOrder = c.completedOrder[1:]
				delete(c.completed, old)
				delete(c.seen, old)
			}
		}
		c.mu.Unlock()
		if err != nil {
			return fmt.Errorf("relay route drain acknowledgement: %w", err)
		}
		return nil
	}
}

func sameDrainOutcome(previous, next domain.RouteDrainState) bool {
	return previous.CanonicalDomain == next.CanonicalDomain && previous.TransitionGeneration == next.TransitionGeneration && previous.OldTargetKey == next.OldTargetKey && previous.InFlight == next.InFlight && previous.TimeoutReason == next.TimeoutReason
}

func (c *DrainCoordinator) Timeout(ctx context.Context, key domain.RouteTargetKey) error {
	c.mu.Lock()
	pending, exists := c.pending[key]
	if !exists {
		if c.completed[key] != nil {
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		return ErrDrainUnknown
	}
	if pending.status != domain.RouteDrainStatusPending {
		c.mu.Unlock()
		return nil
	}
	state := pending.state
	c.mu.Unlock()
	state.InFlight = 1
	state.TimeoutReason = domain.RouteDrainTimeoutReasonControl
	return c.relay(ctx, state)
}
