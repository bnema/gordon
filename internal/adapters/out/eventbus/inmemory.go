// Package eventbus implements the event bus adapter.
package eventbus

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// InMemory implements the EventBus interface using in-memory channels.
type InMemory struct {
	handlers   []out.EventHandler
	eventChan  chan domain.Event
	done       chan struct{}
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	bufferSize int
	log        zerowrap.Logger
	metrics    *telemetry.Metrics
}

// SetMetrics sets the telemetry metrics for the event bus.
// Must be called before Start() to avoid data races on bus.metrics reads.
func (bus *InMemory) SetMetrics(m *telemetry.Metrics) {
	bus.mu.Lock()
	bus.metrics = m
	bus.mu.Unlock()
}

// NewInMemory creates a new in-memory event bus.
func NewInMemory(bufferSize int, log zerowrap.Logger) *InMemory {
	if bufferSize <= 0 {
		bufferSize = 100
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &InMemory{
		handlers:   make([]out.EventHandler, 0),
		eventChan:  make(chan domain.Event, bufferSize),
		done:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		bufferSize: bufferSize,
		log:        log,
	}
}

// Publish publishes an event to the bus.
func (bus *InMemory) Publish(eventType domain.EventType, payload any) error {
	event := domain.Event{
		ID:        uuid.New().String(),
		Type:      eventType,
		Timestamp: time.Now(),
		Data:      payload,
	}

	// Add specific fields for known event types
	switch p := payload.(type) {
	case domain.ImagePushedPayload:
		event.ImageName = p.Name
		event.Tag = p.Reference
	}

	select {
	case bus.eventChan <- event:
		bus.log.Debug().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "eventbus").
			Str("event_id", event.ID).
			Str(zerowrap.FieldEvent, string(event.Type)).
			Str("image_name", event.ImageName).
			Str("tag", event.Tag).
			Msg("event published")
		return nil
	case <-bus.ctx.Done():
		return fmt.Errorf("event bus is stopped")
	case <-time.After(5 * time.Second):
		bus.log.Error().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "eventbus").
			Str("event_id", event.ID).
			Str(zerowrap.FieldEvent, string(event.Type)).
			Str("image_name", event.ImageName).
			Str("tag", event.Tag).
			Msg("event channel is full, dropping event after 5s timeout")

		// Record dropped event metric
		if bus.metrics != nil {
			bus.metrics.EventsDropped.Add(context.Background(), 1, metric.WithAttributes(
				attribute.String("event_type", string(event.Type)),
			))
		}
		return fmt.Errorf("event channel is full, dropping event %s", event.ID)
	}
}

// Subscribe adds an event handler to the bus.
func (bus *InMemory) Subscribe(handler out.EventHandler) error {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	bus.handlers = append(bus.handlers, handler)
	bus.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "eventbus").
		Str(zerowrap.FieldHandler, fmt.Sprintf("%T", handler)).
		Int("total_handlers", len(bus.handlers)).
		Msg("event handler subscribed")

	return nil
}

// Unsubscribe removes an event handler from the bus.
func (bus *InMemory) Unsubscribe(handler out.EventHandler) error {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	for i, h := range bus.handlers {
		if h == handler {
			bus.handlers = append(bus.handlers[:i], bus.handlers[i+1:]...)
			bus.log.Debug().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "eventbus").
				Str(zerowrap.FieldHandler, fmt.Sprintf("%T", handler)).
				Int("total_handlers", len(bus.handlers)).
				Msg("event handler unsubscribed")
			return nil
		}
	}

	return fmt.Errorf("handler not found")
}

// Start starts the event bus processing loop.
func (bus *InMemory) Start() error {
	bus.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "eventbus").
		Int("buffer_size", bus.bufferSize).
		Msg("starting event bus")

	go bus.processEvents()
	return nil
}

// Stop stops the event bus.
func (bus *InMemory) Stop() error {
	bus.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "eventbus").
		Msg("stopping event bus")

	bus.cancel()

	select {
	case <-bus.done:
		bus.log.Info().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "eventbus").
			Msg("event bus stopped")
		return nil
	case <-time.After(5 * time.Second):
		bus.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "eventbus").
			Msg("event bus stop timeout")
		return fmt.Errorf("timeout waiting for event bus to stop")
	}
}

func (bus *InMemory) processEvents() {
	defer close(bus.done)

	for {
		select {
		case event := <-bus.eventChan:
			bus.handleEvent(event)
		case <-bus.ctx.Done():
			bus.log.Debug().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "eventbus").
				Msg("event bus processing stopped")
			return
		}
	}
}

func (bus *InMemory) handleEvent(event domain.Event) {
	bus.mu.RLock()
	handlers := make([]out.EventHandler, len(bus.handlers))
	copy(handlers, bus.handlers)
	bus.mu.RUnlock()

	for _, handler := range handlers {
		if handler.CanHandle(event.Type) {
			h := handler // shadow loop variable for goroutine capture
			start := time.Now()

			ctx, cancel := context.WithTimeout(bus.ctx, 30*time.Second)

			done := make(chan error, 1)
			go func() {
				done <- h.Handle(ctx, event)
			}()

			select {
			case err := <-done:
				cancel()
				if err != nil {
					bus.log.Error().
						Str(zerowrap.FieldLayer, "adapter").
						Str(zerowrap.FieldAdapter, "eventbus").
						Err(err).
						Str("event_id", event.ID).
						Str(zerowrap.FieldEvent, string(event.Type)).
						Str(zerowrap.FieldHandler, fmt.Sprintf("%T", h)).
						Msg("error handling event")
				} else {
					bus.log.Debug().
						Str(zerowrap.FieldLayer, "adapter").
						Str(zerowrap.FieldAdapter, "eventbus").
						Str("event_id", event.ID).
						Str(zerowrap.FieldEvent, string(event.Type)).
						Str(zerowrap.FieldHandler, fmt.Sprintf("%T", h)).
						Dur(zerowrap.FieldDuration, time.Since(start)).
						Msg("event handled successfully")

					// Record processed event metric
					if bus.metrics != nil {
						bus.metrics.EventsProcessed.Add(context.Background(), 1, metric.WithAttributes(
							attribute.String("event_type", string(event.Type)),
						))
					}
				}
			case <-ctx.Done():
				cancel()
				bus.log.Warn().
					Str(zerowrap.FieldLayer, "adapter").
					Str(zerowrap.FieldAdapter, "eventbus").
					Str("event_id", event.ID).
					Str(zerowrap.FieldEvent, string(event.Type)).
					Str(zerowrap.FieldHandler, fmt.Sprintf("%T", h)).
					Dur(zerowrap.FieldDuration, time.Since(start)).
					Msg("handler timeout after 30s")
			}
		}
	}
}
