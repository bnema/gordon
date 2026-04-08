package middleware

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	out "github.com/bnema/gordon/internal/boundaries/out"
)

// AccessLogger is a middleware that writes one access-log entry per HTTP request
// to the provided writer. It runs alongside (not instead of) RequestLogger.
//
// When excludeHealthChecks is true, requests from the Gordon health prober
// (UA prefix out.HealthCheckUserAgentPrefix) or from loopback IPs are not logged.
//
// Write failures are reported as warnings through the application logger and
// never fail the HTTP response.
func AccessLogger(writer out.AccessLogWriter, excludeHealthChecks bool, log zerowrap.Logger, trustedNets []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Ensure a request ID exists before calling inner handlers so both
			// this middleware and RequestLogger share the same value.
			requestID, r := ensureRequestID(w, r)

			// Wrap the response writer to capture status code and bytes written.
			rw := NewResponseWriter(w)

			// Resolve client IP before calling the inner handler so we see the
			// original remote address before any mutation.
			clientIP := GetClientIP(r, trustedNets)

			next.ServeHTTP(rw, r)

			// Apply health-check exclusion after the response so the request
			// always completes — we just skip writing the log entry.
			// UA check uses the shared domain constant so the prober and this
			// filter can never drift. Loopback check reuses localhostNets from
			// cidr.go (same package) — no separate loopback definition needed.
			if excludeHealthChecks {
				if strings.HasPrefix(r.UserAgent(), out.HealthCheckUserAgentPrefix) ||
					httphelper.IsTrustedProxy(clientIP, localhostNets) {
					return
				}
			}

			// Capture end time once; use it for both duration and entry timestamp
			// so the two values are derived from the same instant.
			end := time.Now()
			durationMS := float64(end.Sub(start).Microseconds()) / 1000.0

			entry := out.AccessLogEntry{
				Time:       end.UTC(),
				ClientIP:   clientIP,
				Method:     r.Method,
				Host:       r.Host,
				Path:       r.URL.Path,
				Query:      sanitizeLoggedQuery(r.URL.RawQuery),
				Status:     rw.StatusCode(),
				BytesSent:  rw.BytesWritten(),
				DurationMS: durationMS,
				UserAgent:  r.UserAgent(),
				Referer:    r.Referer(),
				RequestID:  requestID,
				Proto:      r.Proto,
			}

			if err := writer.Write(entry); err != nil {
				log.Warn().
					Str(zerowrap.FieldLayer, "adapter").
					Str(zerowrap.FieldAdapter, "http").
					Str(zerowrap.FieldRequestID, requestID).
					Str(zerowrap.FieldMethod, r.Method).
					Str(zerowrap.FieldPath, r.URL.Path).
					Int(zerowrap.FieldStatus, rw.StatusCode()).
					Str(zerowrap.FieldClientIP, clientIP).
					Err(err).
					Msg("access log write failed")
			}
		})
	}
}

func sanitizeLoggedQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}

	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		if part == "" {
			continue
		}

		key, _, found := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}

		if !isSensitiveQueryKey(decodedKey) {
			continue
		}

		if found {
			parts[i] = key + "=[REDACTED]"
		} else {
			parts[i] = key
		}
	}

	return strings.Join(parts, "&")
}

func isSensitiveQueryKey(key string) bool {
	switch strings.ToLower(key) {
	case "access_token", "auth", "code", "key", "password", "refresh_token", "reset", "secret", "sig", "signature", "token":
		return true
	default:
		return false
	}
}
