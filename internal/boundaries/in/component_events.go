package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// ComponentEventHandler is an inbound application port for handling component event envelopes.
type ComponentEventHandler interface {
	HandleComponentEvent(ctx context.Context, event domain.ComponentEventEnvelope) error
}
