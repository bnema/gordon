package logexport

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// HostOwnerResolver maps a canonical HTTP host to the app service that
// serves it. apptraffic.HostIndex implements it.
type HostOwnerResolver interface {
	HostOwner(host string) (app, service string, ok bool)
}

// AccessLogExporter exports proxy access entries under the identity of
// the app service that owns the requested host, so one app's access
// and container logs share one source. Unowned hosts (registry, admin,
// unknown) are exported as Gordon's own records.
type AccessLogExporter struct {
	hosts    HostOwnerResolver
	exporter out.LogExporter
	next     out.AccessLogWriter
}

var _ out.AccessLogWriter = (*AccessLogExporter)(nil)

// NewAccessLogExporter creates the exporter. next, when non-nil, also
// receives every entry (the local access log sink).
func NewAccessLogExporter(hosts HostOwnerResolver, exporter out.LogExporter, next out.AccessLogWriter) *AccessLogExporter {
	return &AccessLogExporter{hosts: hosts, exporter: exporter, next: next}
}

// Write implements out.AccessLogWriter. Export never fails the request;
// only the local sink can report an error.
func (a *AccessLogExporter) Write(entry out.AccessLogEntry) error {
	a.exporter.Export(context.Background(), a.record(entry))
	if a.next != nil {
		return a.next.Write(entry)
	}
	return nil
}

func (a *AccessLogExporter) record(entry out.AccessLogEntry) domain.LogRecord {
	host := domain.CanonicalHTTPHost(stripPort(entry.Host))
	path := strings.ToValidUTF8(entry.Path, "\uFFFD")
	userAgent := strings.ToValidUTF8(entry.UserAgent, "\uFFFD")
	var source domain.LogSource
	if app, service, ok := a.hosts.HostOwner(host); ok {
		source = domain.LogSource{App: app, Service: service}
	}
	return domain.LogRecord{
		Time:     entry.Time,
		Source:   source,
		Type:     domain.LogTypeAccess,
		Severity: accessSeverity(entry.Status),
		Body:     fmt.Sprintf("%s %s%s %d", entry.Method, host, path, entry.Status),
		Attributes: map[string]string{
			"http.request.method":         entry.Method,
			"http.response.status_code":   strconv.Itoa(entry.Status),
			"server.address":              host,
			"url.path":                    path,
			"client.address":              entry.ClientIP,
			"user_agent.original":         userAgent,
			"http.request.header.referer": entry.Referer,
			"network.protocol.name":       entry.Proto,
			"http.response.body.size":     strconv.Itoa(entry.BytesSent),
			"http.server.duration_ms":     strconv.FormatFloat(entry.DurationMS, 'f', 3, 64),
			"request.id":                  entry.RequestID,
		},
	}
}

func stripPort(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return hostport
}

func accessSeverity(status int) domain.LogSeverity {
	switch {
	case status >= 500:
		return domain.LogSeverityError
	case status >= 400:
		return domain.LogSeverityWarn
	default:
		return domain.LogSeverityInfo
	}
}
