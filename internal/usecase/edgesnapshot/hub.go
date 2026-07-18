// Package edgesnapshot owns the in-memory route snapshot source used by control.
package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

// ErrNoSnapshot indicates that no route snapshot has been published yet.
var ErrNoSnapshot = errors.New("route snapshot has not been published")

// Source provides immutable snapshots to control-plane transports.
type Source interface {
	Current(context.Context) (domain.RouteTargetSnapshot, error)
	Subscribe(context.Context) (<-chan domain.RouteTargetSnapshot, error)
}

// TransitionObserver prepares control-owned transition state before a snapshot
// becomes visible to edge subscribers. It must respect ctx cancellation.
type TransitionObserver func(context.Context, *domain.RouteTargetSnapshot, domain.RouteTargetSnapshot)

type transitionObserver struct {
	ctx      context.Context
	observer TransitionObserver
}

// SnapshotHub is the control-owned, in-memory route snapshot source. It has no
// runtime dependency. Subscribers receive at most one pending snapshot; a new
// publication replaces an older pending one.
type SnapshotHub struct {
	// publishMu serializes the entire prepare/commit publication flow. State is
	// deliberately unlocked while transition observers perform external work.
	publishMu   sync.Mutex
	mu          sync.Mutex
	current     *domain.RouteTargetSnapshot
	subscribers map[uint64]chan domain.RouteTargetSnapshot
	observers   map[uint64]transitionObserver
	nextID      uint64
}

// NewSnapshotHub returns an empty snapshot hub.
func NewSnapshotHub() *SnapshotHub {
	return &SnapshotHub{subscribers: make(map[uint64]chan domain.RouteTargetSnapshot), observers: make(map[uint64]transitionObserver)}
}

// Publish validates and atomically publishes a strictly newer split-reachable snapshot.
func (h *SnapshotHub) Publish(snapshot domain.RouteTargetSnapshot) error {
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return fmt.Errorf("invalid route snapshot: %w", err)
	}
	clone := snapshot.Clone()

	h.publishMu.Lock()
	defer h.publishMu.Unlock()

	// Capture state under the state mutex, then prepare the transition without
	// it. This keeps Current, Subscribe, and observer removal responsive while
	// a runtime registrar is slow or unavailable.
	h.mu.Lock()
	if h.current != nil && clone.Generation <= h.current.Generation {
		h.mu.Unlock()
		return fmt.Errorf("route snapshot generation %d is not newer than %d", clone.Generation, h.current.Generation)
	}
	var previous *domain.RouteTargetSnapshot
	if h.current != nil {
		previousClone := h.current.Clone()
		previous = &previousClone
	}
	observers := make([]transitionObserver, 0, len(h.observers))
	for _, observer := range h.observers {
		observers = append(observers, observer)
	}
	h.mu.Unlock()

	for _, observer := range observers {
		if observer.ctx.Err() == nil {
			observer.observer(observer.ctx, previous, clone)
		}
	}

	// The transition observers have completed their registration attempts, so
	// edge subscribers cannot observe this snapshot before that preparation.
	h.mu.Lock()
	h.current = &clone
	for _, subscriber := range h.subscribers {
		publishLatest(subscriber, clone)
	}
	h.mu.Unlock()
	return nil
}

// ObserveTransitions registers a synchronous control-owned observer. The
// callback runs outside the hub state mutex and receives a context cancelled
// when this registration is removed or its parent context ends.
func (h *SnapshotHub) ObserveTransitions(ctx context.Context, observer TransitionObserver) func() {
	observerCtx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.observers[id] = transitionObserver{ctx: observerCtx, observer: observer}
	h.mu.Unlock()
	return func() {
		cancel()
		h.mu.Lock()
		delete(h.observers, id)
		h.mu.Unlock()
	}
}

// Current returns an independent immutable snapshot clone.
func (h *SnapshotHub) Current(ctx context.Context) (domain.RouteTargetSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.RouteTargetSnapshot{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		return domain.RouteTargetSnapshot{}, ErrNoSnapshot
	}
	return h.current.Clone(), nil
}

// Subscribe returns a bounded latest-wins subscription and immediately queues
// the current snapshot when one is available. The hub closes it on cancellation.
func (h *SnapshotHub) Subscribe(ctx context.Context) (<-chan domain.RouteTargetSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	updates := make(chan domain.RouteTargetSnapshot, 1)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.subscribers[id] = updates
	if h.current != nil {
		publishLatest(updates, *h.current)
	}
	h.mu.Unlock()

	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if subscriber, ok := h.subscribers[id]; ok {
			delete(h.subscribers, id)
			close(subscriber)
		}
		h.mu.Unlock()
	}()
	return updates, nil
}

func publishLatest(subscriber chan domain.RouteTargetSnapshot, snapshot domain.RouteTargetSnapshot) {
	clone := snapshot.Clone()
	select {
	case subscriber <- clone:
		return
	default:
	}
	select {
	case <-subscriber:
	default:
	}
	select {
	case subscriber <- clone:
	default:
	}
}
