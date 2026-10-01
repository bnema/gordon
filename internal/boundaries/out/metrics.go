package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// Metrics records Gordon operational metrics.
// Implementations must be safe for concurrent use.
type Metrics interface {
	// RecordImagePush counts one stored manifest and its size in bytes.
	RecordImagePush(ctx context.Context, name, reference string, sizeBytes int64)
	// RecordEventProcessed counts one event handled successfully.
	RecordEventProcessed(ctx context.Context, eventType domain.EventType)
	// RecordEventDropped counts one event dropped because the bus was full.
	RecordEventDropped(ctx context.Context, eventType domain.EventType)
}
