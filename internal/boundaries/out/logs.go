package out

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// LogExporter ships log records to an external log backend (OTLP).
// Export must not block on the network: implementations buffer and
// drop on overload rather than slow down callers. Safe for concurrent use.
type LogExporter interface {
	Export(ctx context.Context, record domain.LogRecord)
}

// ContainerLogStreamer follows the demultiplexed output of one container.
type ContainerLogStreamer interface {
	// StreamContainerLogs calls emit for every line emitted after since,
	// in order, and blocks until the stream ends (container stopped),
	// ctx is canceled, or an error occurs. A missing container reports
	// domain.ErrContainerNotFound.
	StreamContainerLogs(ctx context.Context, containerID string, since time.Time, emit func(domain.ContainerLogLine)) error
}
