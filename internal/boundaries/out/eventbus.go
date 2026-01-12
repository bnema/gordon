package out

import (
	"gordon/internal/domain"
)

// EventHandler defines the contract for handling events.
type EventHandler interface {
	Handle(event domain.Event) error
	CanHandle(eventType domain.EventType) bool
}

// EventPublisher defines the contract for publishing events.
type EventPublisher interface {
	Publish(eventType domain.EventType, payload any) error
}

// EventSubscriber defines the contract for subscribing to events.
type EventSubscriber interface {
	Subscribe(handler EventHandler) error
	Unsubscribe(handler EventHandler) error
}

// EventBus combines publishing and subscribing capabilities with lifecycle management.
type EventBus interface {
	EventPublisher
	EventSubscriber
	Start() error
	Stop() error
}
