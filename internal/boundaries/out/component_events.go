package out

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// ComponentEventPublisher publishes transport-neutral component event envelopes.
type ComponentEventPublisher interface {
	PublishComponentEvent(ctx context.Context, event domain.ComponentEventEnvelope) error
}

// ComponentEventHandler handles component event envelopes delivered by a subscriber.
type ComponentEventHandler interface {
	HandleComponentEvent(ctx context.Context, event domain.ComponentEventEnvelope) error
}

// ComponentEventSubscriber subscribes handlers to component event delivery.
type ComponentEventSubscriber interface {
	SubscribeComponentEvents(ctx context.Context, handler ComponentEventHandler) error
	UnsubscribeComponentEvents(ctx context.Context, handler ComponentEventHandler) error
}

// ComponentEventAckStore records processed de-dupe keys for local idempotence.
type ComponentEventAckStore interface {
	MarkComponentEventProcessed(ctx context.Context, dedupeKey string, processedAt time.Time) error
	IsComponentEventProcessed(ctx context.Context, dedupeKey string) (bool, error)
}
