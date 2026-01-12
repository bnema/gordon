package out

import (
	"context"
	"io"
)

// ContainerLogWriter defines the contract for container log collection.
// This interface abstracts the storage of container stdout/stderr streams.
type ContainerLogWriter interface {
	// StartLogging begins collecting logs for a container.
	// The logStream should be from ContainerRuntime.GetContainerLogs with follow=true.
	// Domain is used for the log filename.
	StartLogging(ctx context.Context, containerID string, domain string, logStream io.ReadCloser) error

	// StopLogging stops log collection for a container.
	StopLogging(containerID string) error

	// Close stops all logging and releases resources.
	Close() error
}
