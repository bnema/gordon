package accesslog

import (
	"encoding/json"
	"fmt"
	"strings"
)

// jsonEntry is the stable JSON schema for access log entries.
// Field order is controlled by the struct definition.
type jsonEntry struct {
	Time       string  `json:"time"`
	ClientIP   string  `json:"client_ip"`
	Method     string  `json:"method"`
	Host       string  `json:"host"`
	Path       string  `json:"path"`
	Query      string  `json:"query"`
	Status     int     `json:"status"`
	BytesSent  int     `json:"bytes_sent"`
	DurationMS float64 `json:"duration_ms"`
	UserAgent  string  `json:"user_agent"`
	Referer    string  `json:"referer"`
	RequestID  string  `json:"request_id"`
	Proto      string  `json:"proto"`
}

// clfTimestamp is the standard Common Log Format timestamp layout.
const clfTimestamp = "02/Jan/2006:15:04:05 -0700"

// jsonTimestamp is the access log JSON timestamp layout: RFC3339 with
// millisecond precision in UTC.
const jsonTimestamp = "2006-01-02T15:04:05.000Z07:00"

// formatJSON serializes entry as a single-line JSON object.
// Timestamps are RFC3339 with millisecond precision in UTC.
// time.Time's default JSON marshaling is NOT used to ensure stable precision.
func formatJSON(e Entry) (string, error) {
	je := jsonEntry{
		Time:       e.Time.UTC().Format(jsonTimestamp),
		ClientIP:   e.ClientIP,
		Method:     e.Method,
		Host:       e.Host,
		Path:       e.Path,
		Query:      e.Query,
		Status:     e.Status,
		BytesSent:  e.BytesSent,
		DurationMS: e.DurationMS,
		UserAgent:  e.UserAgent,
		Referer:    e.Referer,
		RequestID:  e.RequestID,
		Proto:      e.Proto,
	}
	b, err := json.Marshal(je)
	if err != nil {
		return "", fmt.Errorf("formatJSON: %w", err)
	}
	return string(b), nil
}

// formatCLF serializes entry in Common Log Format (NCSA).
//
//	<ip> - - [timestamp] "METHOD target PROTO" status bytes
func formatCLF(e Entry) (string, error) {
	ts := e.Time.UTC().Format(clfTimestamp)
	target := buildRequestTarget(e.Path, e.Query)
	return fmt.Sprintf(`%s - - [%s] "%s %s %s" %d %d`,
		e.ClientIP,
		ts,
		escapeQuoted(e.Method),
		escapeQuoted(target),
		escapeQuoted(e.Proto),
		e.Status,
		e.BytesSent,
	), nil
}

// formatCombined serializes entry in Combined Log Format (CLF + referer + UA).
//
//	<ip> - - [timestamp] "METHOD target PROTO" status bytes "referer" "ua"
func formatCombined(e Entry) (string, error) {
	ts := e.Time.UTC().Format(clfTimestamp)
	target := buildRequestTarget(e.Path, e.Query)
	referer := e.Referer
	if referer == "" {
		referer = "-"
	}
	ua := e.UserAgent
	if ua == "" {
		ua = "-"
	}
	return fmt.Sprintf(`%s - - [%s] "%s %s %s" %d %d "%s" "%s"`,
		e.ClientIP,
		ts,
		escapeQuoted(e.Method),
		escapeQuoted(target),
		escapeQuoted(e.Proto),
		e.Status,
		e.BytesSent,
		escapeQuoted(referer),
		escapeQuoted(ua),
	), nil
}

// buildRequestTarget builds the request target portion of a CLF/Combined line.
// When query is empty only the path is used; otherwise "path?query".
func buildRequestTarget(path, query string) string {
	if query == "" {
		return path
	}
	return path + "?" + query
}

// escapeQuoted escapes backslashes, double-quotes, and control characters inside
// a value that will be enclosed in double-quotes in the log line.
// All ASCII control characters (0x00-0x1F) are escaped to prevent log-injection
// via user-controlled fields such as User-Agent or Referer.
func escapeQuoted(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
