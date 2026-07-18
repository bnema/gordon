// Package componentevents owns the control-plane event fan-out and idempotence window.
package componentevents

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

var ErrNoEvent = errors.New("component event has not been published")

type Ack struct {
	EventID   string
	Duplicate bool
}

// EventHub is a bounded latest-wins event source. It stores immutable value
// payloads and bounds both subscribers and de-duplication state.
type EventHub struct {
	mu          sync.Mutex
	latest      *domain.ComponentEventEnvelope
	subscribers map[uint64]chan domain.ComponentEventEnvelope
	nextID      uint64
	capacity    int
	seen        map[string]*list.Element
	lru         *list.List
}

type seenEvent struct{ key string }

func NewEventHub(capacity int) *EventHub {
	if capacity <= 0 {
		capacity = 1024
	}
	return &EventHub{capacity: capacity, subscribers: make(map[uint64]chan domain.ComponentEventEnvelope), seen: make(map[string]*list.Element), lru: list.New()}
}

func (h *EventHub) Publish(ctx context.Context, event domain.ComponentEventEnvelope) (Ack, error) {
	if err := ctx.Err(); err != nil {
		return Ack{}, err
	}
	if err := event.Validate(); err != nil {
		return Ack{}, fmt.Errorf("validate component event: %w", err)
	}
	key := event.DedupeKey()
	h.mu.Lock()
	defer h.mu.Unlock()
	if element, ok := h.seen[key]; ok {
		h.lru.MoveToFront(element)
		return Ack{EventID: event.ID, Duplicate: true}, nil
	}
	clone := cloneEvent(event)
	h.latest = &clone
	element := h.lru.PushFront(seenEvent{key: key})
	h.seen[key] = element
	if h.lru.Len() > h.capacity {
		old := h.lru.Back()
		delete(h.seen, old.Value.(seenEvent).key)
		h.lru.Remove(old)
	}
	for _, subscriber := range h.subscribers {
		publishLatest(subscriber, clone)
	}
	return Ack{EventID: event.ID}, nil
}

func (h *EventHub) Current(ctx context.Context) (domain.ComponentEventEnvelope, error) {
	if err := ctx.Err(); err != nil {
		return domain.ComponentEventEnvelope{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.latest == nil {
		return domain.ComponentEventEnvelope{}, ErrNoEvent
	}
	return cloneEvent(*h.latest), nil
}

// Subscribe immediately queues the latest event, making a reconnect a safe
// synchronization point. A slow consumer receives the newest event only.
func (h *EventHub) Subscribe(ctx context.Context) (<-chan domain.ComponentEventEnvelope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	updates := make(chan domain.ComponentEventEnvelope, 1)
	h.mu.Lock()
	id := h.nextID
	h.nextID++
	h.subscribers[id] = updates
	if h.latest != nil {
		publishLatest(updates, *h.latest)
	}
	h.mu.Unlock()
	go func() {
		<-ctx.Done()
		h.mu.Lock()
		if ch, ok := h.subscribers[id]; ok {
			delete(h.subscribers, id)
			close(ch)
		}
		h.mu.Unlock()
	}()
	return updates, nil
}

func publishLatest(ch chan domain.ComponentEventEnvelope, event domain.ComponentEventEnvelope) {
	event = cloneEvent(event)
	select {
	case ch <- event:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- event:
	default:
	}
}

func cloneEvent(event domain.ComponentEventEnvelope) domain.ComponentEventEnvelope { return event }
