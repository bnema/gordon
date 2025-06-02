package events

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

type InMemoryEventBus struct {
	handlers    []EventHandler
	eventChan   chan Event
	done        chan struct{}
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	bufferSize  int
}

func NewInMemoryEventBus(bufferSize int) *InMemoryEventBus {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	
	return &InMemoryEventBus{
		handlers:   make([]EventHandler, 0),
		eventChan:  make(chan Event, bufferSize),
		done:       make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		bufferSize: bufferSize,
	}
}

func (bus *InMemoryEventBus) Publish(event Event) error {
	if event.ID == "" {
		event.ID = uuid.New().String()
	}
	
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	select {
	case bus.eventChan <- event:
		log.Debug().
			Str("event_id", event.ID).
			Str("event_type", string(event.Type)).
			Str("image_name", event.ImageName).
			Str("tag", event.Tag).
			Msg("Event published")
		return nil
	case <-bus.ctx.Done():
		return fmt.Errorf("event bus is stopped")
	default:
		return fmt.Errorf("event channel is full, dropping event %s", event.ID)
	}
}

func (bus *InMemoryEventBus) Subscribe(handler EventHandler) error {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	
	bus.handlers = append(bus.handlers, handler)
	log.Debug().
		Str("handler_type", fmt.Sprintf("%T", handler)).
		Int("total_handlers", len(bus.handlers)).
		Msg("Event handler subscribed")
	
	return nil
}

func (bus *InMemoryEventBus) Unsubscribe(handler EventHandler) error {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	
	for i, h := range bus.handlers {
		if h == handler {
			bus.handlers = append(bus.handlers[:i], bus.handlers[i+1:]...)
			log.Debug().
				Str("handler_type", fmt.Sprintf("%T", handler)).
				Int("total_handlers", len(bus.handlers)).
				Msg("Event handler unsubscribed")
			return nil
		}
	}
	
	return fmt.Errorf("handler not found")
}

func (bus *InMemoryEventBus) Start() error {
	log.Info().
		Int("buffer_size", bus.bufferSize).
		Msg("Starting event bus")
	
	go bus.processEvents()
	return nil
}

func (bus *InMemoryEventBus) Stop() error {
	log.Info().Msg("Stopping event bus")
	
	bus.cancel()
	
	select {
	case <-bus.done:
		log.Info().Msg("Event bus stopped")
		return nil
	case <-time.After(5 * time.Second):
		log.Warn().Msg("Event bus stop timeout")
		return fmt.Errorf("timeout waiting for event bus to stop")
	}
}

func (bus *InMemoryEventBus) processEvents() {
	defer close(bus.done)
	
	for {
		select {
		case event := <-bus.eventChan:
			bus.handleEvent(event)
		case <-bus.ctx.Done():
			log.Debug().Msg("Event bus processing stopped")
			return
		}
	}
}

func (bus *InMemoryEventBus) handleEvent(event Event) {
	bus.mu.RLock()
	handlers := make([]EventHandler, len(bus.handlers))
	copy(handlers, bus.handlers)
	bus.mu.RUnlock()
	
	for _, handler := range handlers {
		if handler.CanHandle(event.Type) {
			go func(h EventHandler) {
				start := time.Now()
				
				if err := h.Handle(event); err != nil {
					log.Error().
						Err(err).
						Str("event_id", event.ID).
						Str("event_type", string(event.Type)).
						Str("handler_type", fmt.Sprintf("%T", h)).
						Msg("Error handling event")
				} else {
					log.Debug().
						Str("event_id", event.ID).
						Str("event_type", string(event.Type)).
						Str("handler_type", fmt.Sprintf("%T", h)).
						Dur("duration", time.Since(start)).
						Msg("Event handled successfully")
				}
			}(handler)
		}
	}
}