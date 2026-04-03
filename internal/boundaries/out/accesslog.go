package out

import "time"

// AccessLogEntry represents a single HTTP access log record.
// It is the neutral data contract between the access log middleware (adapters/in)
// and the access log writer (adapters/out). Keeping it in the boundaries layer
// prevents direct adapter-to-adapter coupling.
type AccessLogEntry struct {
	Time       time.Time
	ClientIP   string
	Method     string
	Host       string
	Path       string
	Query      string
	Status     int
	BytesSent  int
	DurationMS float64
	UserAgent  string
	Referer    string
	RequestID  string
	Proto      string
}

// AccessLogWriter writes HTTP access log entries to a configured sink.
// *accesslog.Writer in internal/adapters/out/accesslog implements this interface.
type AccessLogWriter interface {
	Write(entry AccessLogEntry) error
}

// HealthCheckUserAgentPrefix is the User-Agent prefix set by Gordon's internal
// health-check prober. Access log middleware uses this to filter out health-check
// noise; the prober uses it to construct the full User-Agent string.
const HealthCheckUserAgentPrefix = "Gordon-HealthCheck/"
