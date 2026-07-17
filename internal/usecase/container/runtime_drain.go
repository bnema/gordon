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
	// ErrRuntimeDrainUnknown means control relayed an acknowledgement for a target
	// that runtime has not prepared for retirement.
	ErrRuntimeDrainUnknown = errors.New("unknown runtime drain")
	// ErrRuntimeDrainStale means an acknowledgement cannot advance the recorded
	// control transition for its opaque target.
	ErrRuntimeDrainStale = errors.New("stale runtime drain")
	// ErrRuntimeDrainConflicting means a duplicate acknowledgement changed its outcome.
	ErrRuntimeDrainConflicting = errors.New("conflicting runtime drain acknowledgement")
)

// RuntimeDrainTargetResolver obtains the sanitized route state used by runtime
// snapshot production. It deliberately returns no raw identity to the edge
// protocol; RuntimeDrainRegistry immediately hashes it into a TargetKey.
type RuntimeDrainTargetResolver func(containerID string) (domain.RuntimeRouteState, bool)

type runtimeDrainIdentity struct {
	domain string
	key    domain.RouteTargetKey
}

type runtimeDrainPending struct {
	containerID string
	done        chan struct{}
	result      bool
	completed   bool
}

type runtimeDrainOutcome struct {
	generation domain.RouteTargetGeneration
	result     bool
	inFlight   uint64
	timeout    domain.RouteDrainTimeoutReason
}

// RuntimeDrainRegistry is the runtime-side endpoint of the split drain
// protocol. Lifecycle callers use private container IDs only locally; control
// acknowledgements are matched solely by canonical domain and opaque TargetKey.
type RuntimeDrainRegistry struct {
	resolve RuntimeDrainTargetResolver

	mu                 sync.Mutex
	pending            map[runtimeDrainIdentity]*runtimeDrainPending
	pendingByContainer map[string]runtimeDrainIdentity
	completed          map[runtimeDrainIdentity]runtimeDrainOutcome
	lastGeneration     map[runtimeDrainIdentity]domain.RouteTargetGeneration
	closed             bool
}

var _ out.ProxyDrainWaiter = (*RuntimeDrainRegistry)(nil)
var _ out.RouteDrainAckReceiver = (*RuntimeDrainRegistry)(nil)

// runtimeRemoteDrainWaiter marks the split-only waiter so Service can retain
// monolith cache-invalidation semantics while allowing runtime snapshots to
// perform the remote traffic switch.
type runtimeRemoteDrainWaiter interface {
	runtimeRemoteDrainWaiter()
}

func (*RuntimeDrainRegistry) runtimeRemoteDrainWaiter() {}

// NewRuntimeDrainRegistry creates a runtime-local drain waiter. The resolver
// must use the same route-state construction as the runtime snapshot producer.
func NewRuntimeDrainRegistry(resolve RuntimeDrainTargetResolver) *RuntimeDrainRegistry {
	return &RuntimeDrainRegistry{
		resolve:            resolve,
		pending:            make(map[runtimeDrainIdentity]*runtimeDrainPending),
		pendingByContainer: make(map[string]runtimeDrainIdentity),
		completed:          make(map[runtimeDrainIdentity]runtimeDrainOutcome),
		lastGeneration:     make(map[runtimeDrainIdentity]domain.RouteTargetGeneration),
	}
}

// PrepareDrain resolves and pins the old target before the traffic switch. A
// later control acknowledgement may therefore safely arrive before WaitForNoInFlight.
func (r *RuntimeDrainRegistry) PrepareDrain(containerID string) {
	if r == nil || containerID == "" || r.resolve == nil {
		return
	}
	state, ok := r.resolve(containerID)
	if !ok {
		return
	}
	key, err := domain.ManagedRouteTargetKeyFromRuntimeState(state)
	if err != nil {
		return
	}
	canonicalDomain, ok := domain.CanonicalRouteDomain(state.Domain)
	if !ok {
		return
	}
	identity := runtimeDrainIdentity{domain: canonicalDomain, key: key}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if _, exists := r.pendingByContainer[containerID]; exists {
		return
	}
	if _, exists := r.pending[identity]; exists {
		return
	}
	r.pending[identity] = &runtimeDrainPending{containerID: containerID, done: make(chan struct{})}
	r.pendingByContainer[containerID] = identity
}

// CancelDrain releases a registration on rollback. Any later acknowledgement is
// unknown or stale and cannot affect a subsequent replacement.
func (r *RuntimeDrainRegistry) CancelDrain(containerID string) {
	if r == nil || containerID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseContainerLocked(containerID)
}

// WaitForNoInFlight waits for a matching control-relayed zero-in-flight ack.
// Timeout reports, local timeout, cancellation, and shutdown all return false
// so container cleanup retains its existing warning/fallback behavior.
func (r *RuntimeDrainRegistry) WaitForNoInFlight(ctx context.Context, containerID string, timeout time.Duration) bool {
	if r == nil || containerID == "" {
		return false
	}
	r.PrepareDrain(containerID)

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

// AcknowledgeRouteDrain receives only the control-validated opaque result.
// The transition generation is monotonic per domain/key, independent of the
// runtime polling generation, and duplicate delivery is idempotent.
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
	pending, pendingExists := r.pending[identity]
	generation := r.lastGeneration[identity]
	if !pendingExists {
		if previous, completed := r.completed[identity]; completed && previous == outcome {
			return nil
		}
		if generation >= acknowledgement.TransitionGeneration {
			return ErrRuntimeDrainStale
		}
		return ErrRuntimeDrainUnknown
	}
	if generation >= acknowledgement.TransitionGeneration {
		if previous, completed := r.completed[identity]; completed && previous == outcome {
			return nil
		}
		if generation == acknowledgement.TransitionGeneration {
			return ErrRuntimeDrainConflicting
		}
		return ErrRuntimeDrainStale
	}
	r.lastGeneration[identity] = acknowledgement.TransitionGeneration
	r.completed[identity] = outcome
	pending.result = result
	pending.completed = true
	close(pending.done)
	return nil
}

// Close releases all waiters during runtime shutdown without treating shutdown
// as a clean drain acknowledgement.
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
			pending.result = false
			pending.completed = true
			close(pending.done)
		}
	}
	r.pending = make(map[runtimeDrainIdentity]*runtimeDrainPending)
	r.pendingByContainer = make(map[string]runtimeDrainIdentity)
}

func (r *RuntimeDrainRegistry) releaseContainerLocked(containerID string) {
	identity, exists := r.pendingByContainer[containerID]
	if !exists {
		return
	}
	pending := r.pending[identity]
	if pending != nil && !pending.completed {
		pending.result = false
		pending.completed = true
		close(pending.done)
	}
	delete(r.pendingByContainer, containerID)
	delete(r.pending, identity)
}
