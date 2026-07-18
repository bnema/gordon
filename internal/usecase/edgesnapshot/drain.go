package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const (
	maxDrainLedgerEntries           = 1024
	defaultDrainRegistrationTimeout = 5 * time.Second
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
	// RegistrationTimeout bounds runtime transition preparation so a failed
	// runtime cannot hold control snapshot publication or shutdown indefinitely.
	RegistrationTimeout time.Duration
}

// DrainCoordinator records edge observation separately from runtime delivery:
// a drain is terminal only after runtime has accepted its relay.
type DrainCoordinator struct {
	mu                  sync.Mutex
	expected            string
	now                 func() time.Time
	runtime             RuntimeDrainAckReceiver
	pending             map[domain.RouteTargetKey]*pendingDrain
	completed           map[domain.RouteTargetKey]*pendingDrain
	completedOrder      []domain.RouteTargetKey
	seen                map[domain.RouteTargetKey]domain.RouteTargetGeneration
	registrationTimeout time.Duration
	lifecycle           context.Context
	cancel              context.CancelFunc
	stop                func()
	closeOnce           sync.Once
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
	if options.RegistrationTimeout <= 0 {
		options.RegistrationTimeout = defaultDrainRegistrationTimeout
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	coordinator := &DrainCoordinator{
		expected: expected, now: options.Now, runtime: options.Runtime,
		pending: make(map[domain.RouteTargetKey]*pendingDrain), completed: make(map[domain.RouteTargetKey]*pendingDrain),
		seen: make(map[domain.RouteTargetKey]domain.RouteTargetGeneration), registrationTimeout: options.RegistrationTimeout,
		lifecycle: lifecycle, cancel: cancel,
	}
	coordinator.stop = hub.ObserveTransitions(lifecycle, coordinator.observeTransition)
	return coordinator, nil
}

// Close cancels an in-flight runtime registration before unregistering the
// observer. It never waits for the publication goroutine to finish.
func (c *DrainCoordinator) Close() {
	c.closeOnce.Do(func() {
		c.cancel()
		c.stop()
	})
}

func (c *DrainCoordinator) observeTransition(ctx context.Context, previous *domain.RouteTargetSnapshot, current domain.RouteTargetSnapshot) {
	if previous == nil || ctx.Err() != nil {
		return
	}
	// A transition has one registration budget. This bounds the entire
	// serialized preparation phase even when a snapshot retires many targets.
	registrationCtx, cancelRegistration := context.WithTimeout(ctx, c.registrationTimeout)
	defer cancelRegistration()
	currentKeys := readyOrDrainingTargetKeys(current)
	for _, old := range previous.Entries {
		if registrationCtx.Err() != nil {
			return
		}
		if !retiredTarget(old, current.Generation, currentKeys) || c.transitionSeen(old.TargetKey) {
			continue
		}
		if !c.prepareTransition(registrationCtx, old, current.Generation) {
			if registrationCtx.Err() != nil {
				return
			}
			continue
		}
		c.admitTransition(old, current.Generation)
	}
}

func readyOrDrainingTargetKeys(snapshot domain.RouteTargetSnapshot) map[domain.RouteTargetKey]struct{} {
	keys := make(map[domain.RouteTargetKey]struct{}, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if entry.Ready() || entry.Draining() {
			keys[entry.TargetKey] = struct{}{}
		}
	}
	return keys
}

func retiredTarget(old domain.RouteTargetEntry, generation domain.RouteTargetGeneration, currentKeys map[domain.RouteTargetKey]struct{}) bool {
	if !old.Ready() || old.Generation >= generation {
		return false
	}
	_, stillCurrent := currentKeys[old.TargetKey]
	return !stillCurrent
}

func (c *DrainCoordinator) transitionSeen(key domain.RouteTargetKey) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.seen[key]
	return exists
}

// prepareTransition attempts runtime registration before the snapshot is edge-visible.
func (c *DrainCoordinator) prepareTransition(ctx context.Context, old domain.RouteTargetEntry, generation domain.RouteTargetGeneration) bool {
	registrar, ok := c.runtime.(RuntimeDrainRegistrar)
	if !ok {
		return true
	}
	attemptCtx, cancel := context.WithTimeout(ctx, c.registrationTimeout)
	defer cancel()
	return registrar.PrepareRouteDrain(attemptCtx, old.CanonicalDomain, generation, old.TargetKey) == nil && ctx.Err() == nil
}

func (c *DrainCoordinator) admitTransition(old domain.RouteTargetEntry, generation domain.RouteTargetGeneration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.seen[old.TargetKey]; !exists && len(c.pending) < maxDrainLedgerEntries {
		c.pending[old.TargetKey] = &pendingDrain{state: domain.RouteDrainState{CanonicalDomain: old.CanonicalDomain, TransitionGeneration: generation, OldTargetKey: old.TargetKey}, status: domain.RouteDrainStatusPending}
		c.seen[old.TargetKey] = generation
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
