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

// SnapshotHub is the control-owned, in-memory route snapshot source. It has no
// runtime dependency. Subscribers receive at most one pending snapshot; a new
// publication replaces an older pending one.
type SnapshotHub struct {
	mu          sync.Mutex
	current     *domain.RouteTargetSnapshot
	subscribers map[uint64]chan domain.RouteTargetSnapshot
	nextID      uint64
}

// NewSnapshotHub returns an empty snapshot hub.
func NewSnapshotHub() *SnapshotHub {
	return &SnapshotHub{subscribers: make(map[uint64]chan domain.RouteTargetSnapshot)}
}

// Publish validates and atomically publishes a strictly newer split-reachable snapshot.
func (h *SnapshotHub) Publish(snapshot domain.RouteTargetSnapshot) error {
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return fmt.Errorf("invalid route snapshot: %w", err)
	}
	clone := snapshot.Clone()

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != nil && clone.Generation <= h.current.Generation {
		return fmt.Errorf("route snapshot generation %d is not newer than %d", clone.Generation, h.current.Generation)
	}
	h.current = &clone
	for _, subscriber := range h.subscribers {
		publishLatest(subscriber, clone)
	}
	return nil
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
