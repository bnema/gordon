package container

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

var (
	ErrRuntimeDrainUnknown     = errors.New("unknown runtime drain")
	ErrRuntimeDrainStale       = errors.New("stale runtime drain")
	ErrRuntimeDrainConflicting = errors.New("conflicting runtime drain acknowledgement")
)

const runtimeDrainLedgerLimit = 1024

type RuntimeDrainTargetResolver func(containerID string) (domain.RuntimeRouteState, bool)
type runtimeDrainIdentity struct {
	domain string
	key    domain.RouteTargetKey
}
type runtimeDrainPending struct {
	containerID       string
	done              chan struct{}
	result, completed bool
	generation        domain.RouteTargetGeneration
}
type runtimeDrainOutcome struct {
	generation domain.RouteTargetGeneration
	result     bool
	inFlight   uint64
	timeout    domain.RouteDrainTimeoutReason
}

// RuntimeDrainRegistry joins local lifecycle preparation with control's exact
// transition registration. An acknowledgement is never allowed to infer a
// generation: it must match the latest control registration for its key.
type RuntimeDrainRegistry struct {
	resolve            RuntimeDrainTargetResolver
	mu                 sync.Mutex
	pending            map[runtimeDrainIdentity]*runtimeDrainPending
	pendingByContainer map[string]runtimeDrainIdentity
	completed          map[runtimeDrainIdentity]runtimeDrainOutcome
	lastGeneration     map[runtimeDrainIdentity]domain.RouteTargetGeneration
	// after cancellation a same-key local reprepare must not inherit the old
	// registration while waiting for its replacement generation.
	awaitingNewRegistration map[runtimeDrainIdentity]bool
	completedOrder          []runtimeDrainIdentity
	// awaitingOverflow fails closed when CancelDrain cannot retain another
	// identity. A missing identity must never make an old acknowledgement safe.
	awaitingOverflow bool
	closed           bool
}

var _ out.ProxyDrainWaiter = (*RuntimeDrainRegistry)(nil)
var _ out.RouteDrainAckReceiver = (*RuntimeDrainRegistry)(nil)

type runtimeRemoteDrainWaiter interface{ runtimeRemoteDrainWaiter() }

func (*RuntimeDrainRegistry) runtimeRemoteDrainWaiter() {}

func NewRuntimeDrainRegistry(resolve RuntimeDrainTargetResolver) *RuntimeDrainRegistry {
	return &RuntimeDrainRegistry{resolve: resolve, pending: make(map[runtimeDrainIdentity]*runtimeDrainPending), pendingByContainer: make(map[string]runtimeDrainIdentity), completed: make(map[runtimeDrainIdentity]runtimeDrainOutcome), lastGeneration: make(map[runtimeDrainIdentity]domain.RouteTargetGeneration), awaitingNewRegistration: make(map[runtimeDrainIdentity]bool)}
}

func (r *RuntimeDrainRegistry) PrepareDrain(containerID string) bool {
	if r == nil || containerID == "" || r.resolve == nil {
		return false
	}
	state, ok := r.resolve(containerID)
	if !ok {
		return false
	}
	key, err := domain.ManagedRouteTargetKeyFromRuntimeState(state)
	if err != nil {
		return false
	}
	canonicalDomain, ok := domain.CanonicalRouteDomain(state.Domain)
	if !ok {
		return false
	}
	identity := runtimeDrainIdentity{domain: canonicalDomain, key: key}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	if existing, exists := r.pendingByContainer[containerID]; exists {
		return existing == identity
	}
	if _, exists := r.pending[identity]; exists {
		return false
	}
	pending := &runtimeDrainPending{containerID: containerID, done: make(chan struct{})}
	// Registration may arrive before lifecycle preparation. Do not reuse a
	// registration left behind by a cancelled generation.
	if !r.awaitingNewRegistration[identity] {
		pending.generation = r.lastGeneration[identity]
	}
	if outcome, exists := r.completed[identity]; exists && outcome.generation == pending.generation && pending.generation != 0 {
		pending.completed, pending.result = true, outcome.result
		close(pending.done)
	}
	r.pending[identity], r.pendingByContainer[containerID] = pending, identity
	return true
}

// PrepareRouteDrain is the authenticated control-to-runtime registration RPC
// target. It is idempotent and may arrive before or after PrepareDrain.
func (r *RuntimeDrainRegistry) PrepareRouteDrain(ctx context.Context, canonicalDomain string, generation domain.RouteTargetGeneration, key domain.RouteTargetKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return ErrRuntimeDrainUnknown
	}
	canonicalDomain, ok := domain.CanonicalRouteDomain(canonicalDomain)
	if !ok || generation == 0 || key == "" {
		return ErrRuntimeDrainUnknown
	}
	identity := runtimeDrainIdentity{domain: canonicalDomain, key: key}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrRuntimeDrainUnknown
	}
	previous, known := r.lastGeneration[identity]
	if previous > generation {
		return ErrRuntimeDrainStale
	}
	if previous == generation {
		return nil
	}
	if err := r.canRegisterGenerationLocked(identity, generation, known); err != nil {
		return err
	}
	r.lastGeneration[identity] = generation
	// A newer registration supersedes any terminal outcome for the old
	// generation, while lastGeneration remains its stale-ack tombstone.
	if previous != 0 {
		delete(r.completed, identity)
	}
	delete(r.awaitingNewRegistration, identity)
	if pending := r.pending[identity]; pending != nil {
		pending.generation = generation
		if outcome, exists := r.completed[identity]; exists && outcome.generation == generation {
			pending.completed, pending.result = true, outcome.result
			close(pending.done)
		}
	}
	return nil
}

func (r *RuntimeDrainRegistry) canRegisterGenerationLocked(identity runtimeDrainIdentity, generation domain.RouteTargetGeneration, known bool) error {
	// A generation tombstone is replay protection, not a cache: evicting one
	// would let a delayed acknowledgement become a new legacy acknowledgement.
	// Refuse a new identity once full rather than making that ambiguous.
	if !known && len(r.lastGeneration) >= runtimeDrainLedgerLimit {
		return ErrRuntimeDrainUnknown
	}
	if pending := r.pending[identity]; pending != nil && pending.generation != 0 && pending.generation != generation {
		// Do not advance the registration tombstone until the active waiter can
		// bind to it; otherwise its exact acknowledgement would be rejected.
		return ErrRuntimeDrainStale
	}
	return nil
}

func (r *RuntimeDrainRegistry) CancelDrain(containerID string) {
	if r == nil || containerID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	identity, exists := r.pendingByContainer[containerID]
	if exists {
		if _, awaiting := r.awaitingNewRegistration[identity]; !awaiting && len(r.awaitingNewRegistration) >= runtimeDrainLedgerLimit {
			// The API cannot return an admission error. Keep the overflow as a
			// global fail-closed tombstone so a later reprepare cannot accept an
			// unregistered, delayed acknowledgement.
			r.awaitingOverflow = true
		} else {
			r.awaitingNewRegistration[identity] = true
		}
	}
	r.releaseContainerLocked(containerID)
}

func (r *RuntimeDrainRegistry) WaitForNoInFlight(ctx context.Context, containerID string, timeout time.Duration) bool {
	if r == nil || containerID == "" {
		return false
	}
	r.mu.Lock()
	identity, exists := r.pendingByContainer[containerID]
	if !exists {
		r.mu.Unlock()
		return false
	}
	pending := r.pending[identity]
	if pending == nil {
		r.mu.Unlock()
		return false
	}
	if pending.completed {
		result := pending.result
		r.releaseContainerLocked(containerID)
		r.mu.Unlock()
		return result
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	r.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-pending.done:
		r.mu.Lock()
		result := pending.result
		r.releaseContainerLocked(containerID)
		r.mu.Unlock()
		return result
	case <-ctx.Done():
		r.mu.Lock()
		r.releaseContainerLocked(containerID)
		r.mu.Unlock()
		return false
	case <-timer.C:
		r.mu.Lock()
		r.releaseContainerLocked(containerID)
		r.mu.Unlock()
		return false
	}
}

func (r *RuntimeDrainRegistry) AcknowledgeRouteDrain(ctx context.Context, acknowledgement domain.RouteDrainAck) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return ErrRuntimeDrainUnknown
	}
	if err := acknowledgement.Validate(); err != nil {
		return err
	}
	identity := runtimeDrainIdentity{domain: acknowledgement.CanonicalDomain, key: acknowledgement.OldTargetKey}
	result := acknowledgement.Status == domain.RouteDrainStatusAcknowledged
	outcome := runtimeDrainOutcome{generation: acknowledgement.TransitionGeneration, result: result, inFlight: acknowledgement.InFlight, timeout: acknowledgement.TimeoutReason}
	r.mu.Lock()
	defer r.mu.Unlock()
	registered, err := r.matchAcknowledgementLocked(identity, acknowledgement.TransitionGeneration)
	if err != nil {
		return err
	}
	return r.completeAcknowledgementLocked(identity, registered, outcome)
}

func (r *RuntimeDrainRegistry) matchAcknowledgementLocked(identity runtimeDrainIdentity, generation domain.RouteTargetGeneration) (domain.RouteTargetGeneration, error) {
	// Cancellation invalidates every old acknowledgement, including one equal
	// to the previously registered generation. Only a strictly newer,
	// authenticated registration clears this state.
	if r.awaitingNewRegistration[identity] {
		return 0, ErrRuntimeDrainStale
	}
	registered := r.lastGeneration[identity]
	if registered == 0 {
		// Legacy in-process deployments have no control registration transport.
		if pending := r.pending[identity]; pending == nil || r.awaitingOverflow {
			return 0, ErrRuntimeDrainUnknown
		}
		if len(r.lastGeneration) >= runtimeDrainLedgerLimit {
			return 0, ErrRuntimeDrainUnknown
		}
		r.lastGeneration[identity], registered = generation, generation
	}
	if generation == registered {
		return registered, nil
	}
	if generation < registered {
		return 0, ErrRuntimeDrainStale
	}
	return 0, ErrRuntimeDrainUnknown
}

func (r *RuntimeDrainRegistry) completeAcknowledgementLocked(identity runtimeDrainIdentity, generation domain.RouteTargetGeneration, outcome runtimeDrainOutcome) error {
	if previous, exists := r.completed[identity]; exists {
		if previous == outcome {
			return nil
		}
		return ErrRuntimeDrainConflicting
	}
	pending := r.pending[identity]
	if pending == nil {
		// A terminal outcome that has aged out is deliberately fail-closed:
		// accepting a conflicting retry would be less safe than a timeout.
		return ErrRuntimeDrainUnknown
	}
	if pending.generation != 0 && pending.generation != generation {
		return ErrRuntimeDrainUnknown
	}
	if len(r.completed) >= runtimeDrainLedgerLimit {
		r.trimCompletedToLocked(runtimeDrainLedgerLimit - 1)
		if len(r.completed) >= runtimeDrainLedgerLimit {
			return ErrRuntimeDrainUnknown
		}
	}
	if pending.generation == 0 {
		pending.generation = generation
	}
	r.completed[identity] = outcome
	r.completedOrder = append(r.completedOrder, identity)
	r.trimCompletedLocked()
	pending.result, pending.completed = outcome.result, true
	close(pending.done)
	return nil
}

func (r *RuntimeDrainRegistry) trimCompletedLocked() {
	r.trimCompletedToLocked(runtimeDrainLedgerLimit)
}

func (r *RuntimeDrainRegistry) trimCompletedToLocked(limit int) {
	// Keep FIFO order deterministic while removing stale queue entries.
	order := r.completedOrder[:0]
	for _, identity := range r.completedOrder {
		if _, exists := r.completed[identity]; exists {
			order = append(order, identity)
		}
	}
	r.completedOrder = order
	for len(r.completed) > limit {
		evicted := false
		for index, identity := range r.completedOrder {
			// An active waiter may still need its exact terminal outcome.
			if r.pending[identity] != nil {
				continue
			}
			delete(r.completed, identity)
			r.completedOrder = append(r.completedOrder[:index], r.completedOrder[index+1:]...)
			evicted = true
			break
		}
		if !evicted {
			return
		}
	}
}

func (r *RuntimeDrainRegistry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for _, pending := range r.pending {
		if !pending.completed {
			pending.result, pending.completed = false, true
			close(pending.done)
		}
	}
	r.pending, r.pendingByContainer = make(map[runtimeDrainIdentity]*runtimeDrainPending), make(map[string]runtimeDrainIdentity)
}
func (r *RuntimeDrainRegistry) releaseContainerLocked(containerID string) {
	identity, exists := r.pendingByContainer[containerID]
	if !exists {
		return
	}
	pending := r.pending[identity]
	if pending != nil && !pending.completed {
		pending.result, pending.completed = false, true
		close(pending.done)
	}
	delete(r.pendingByContainer, containerID)
	delete(r.pending, identity)
	r.trimCompletedLocked()
}
