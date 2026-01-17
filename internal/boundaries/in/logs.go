package in

import "context"

// LogService defines the contract for accessing logs.
type LogService interface {
	// GetProcessLogs returns the last N lines of Gordon process logs.
	GetProcessLogs(ctx context.Context, lines int) ([]string, error)

	// FollowProcessLogs returns a channel that streams Gordon process log lines.
	// The channel is closed when the context is canceled.
	FollowProcessLogs(ctx context.Context, initialLines int) (<-chan string, error)

	// GetContainerLogs returns the last N lines of container logs for a domain.
	GetContainerLogs(ctx context.Context, domain string, lines int) ([]string, error)

	// FollowContainerLogs returns a channel that streams container log lines.
	// The channel is closed when the context is canceled.
	FollowContainerLogs(ctx context.Context, domain string, initialLines int) (<-chan string, error)
}
