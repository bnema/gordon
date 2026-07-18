package edgesnapshot

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

// ErrNoTrafficGraph indicates no control-owned traffic graph has been published.
var ErrNoTrafficGraph = errors.New("traffic graph has not been published")

// TrafficGraphSource supplies immutable, sanitized graph snapshots to transport.
type TrafficGraphSource interface {
	CurrentTrafficGraph(context.Context) (domain.TrafficGraphSnapshot, error)
	SubscribeTrafficGraphs(context.Context) (<-chan domain.TrafficGraphSnapshot, error)
}

// TrafficGraphHub stores the newest valid graph. A slow subscriber sees only
// the latest pending graph; mutable input and output are never shared.
type TrafficGraphHub struct {
	mu          sync.Mutex
	current     *domain.TrafficGraphSnapshot
	subscribers map[uint64]chan domain.TrafficGraphSnapshot
	nextID      uint64
}

func NewTrafficGraphHub() *TrafficGraphHub {
	return &TrafficGraphHub{subscribers: make(map[uint64]chan domain.TrafficGraphSnapshot)}
}

// Publish atomically publishes a strictly newer split-reachable graph.
func (h *TrafficGraphHub) Publish(snapshot domain.TrafficGraphSnapshot) error {
	if err := snapshot.ValidateSplitReachability(); err != nil {
		return fmt.Errorf("invalid traffic graph snapshot: %w", err)
	}
	clone := snapshot.Clone()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current != nil && clone.Generation <= h.current.Generation {
		return fmt.Errorf("traffic graph generation %d is not newer than %d", clone.Generation, h.current.Generation)
	}
	h.current = &clone
	for _, subscriber := range h.subscribers {
		publishLatestTrafficGraph(subscriber, clone)
	}
	return nil
}

func (h *TrafficGraphHub) CurrentTrafficGraph(ctx context.Context) (domain.TrafficGraphSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.TrafficGraphSnapshot{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.current == nil {
		return domain.TrafficGraphSnapshot{}, ErrNoTrafficGraph
	}
	return h.current.Clone(), nil
}

func (h *TrafficGraphHub) SubscribeTrafficGraphs(ctx context.Context) (<-chan domain.TrafficGraphSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	updates := make(chan domain.TrafficGraphSnapshot, 1)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.subscribers[id] = updates
	if h.current != nil {
		publishLatestTrafficGraph(updates, *h.current)
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

func publishLatestTrafficGraph(subscriber chan domain.TrafficGraphSnapshot, snapshot domain.TrafficGraphSnapshot) {
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
