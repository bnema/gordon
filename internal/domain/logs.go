package domain

import "time"

// LogSource identifies the workload that emitted a log record.
// A zero LogSource is Gordon itself.
type LogSource struct {
	App     string
	Service string
}

// IsGordon reports whether the record belongs to Gordon itself.
func (s LogSource) IsGordon() bool {
	return s.App == ""
}

// ServiceName is the unique exported service name, "<app>.<service>".
// Service names alone collide across apps (many apps have "web").
func (s LogSource) ServiceName() string {
	if s.IsGordon() {
		return "gordon"
	}
	return s.App + "." + s.Service
}

// Log stream names for container output.
const (
	LogStreamStdout = "stdout"
	LogStreamStderr = "stderr"
)

// Log types distinguish record families sharing one source.
const (
	LogTypeContainer = "container"
	LogTypeAccess    = "access"
)

// LogSeverity is a coarse, exporter-neutral severity.
type LogSeverity int

// Severities. The zero value means unspecified.
const (
	LogSeverityUnspecified LogSeverity = iota
	LogSeverityInfo
	LogSeverityWarn
	LogSeverityError
)

// ContainerLogLine is one demultiplexed line of container output.
type ContainerLogLine struct {
	Time   time.Time
	Stream string
	Body   string
}

// LogRecord is one exported log record.
type LogRecord struct {
	Time     time.Time
	Source   LogSource
	Type     string
	Severity LogSeverity
	Body     string
	// Stream is stdout or stderr for container output, empty otherwise.
	Stream string
	// Attributes carry per-record metadata (status, path...).
	Attributes map[string]string
}
