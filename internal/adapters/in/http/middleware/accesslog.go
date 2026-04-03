package middleware

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/accesslog"
)

// loopbackNets contains the loopback ranges used for health-check exclusion.
var loopbackNets = func() []*net.IPNet {
	_, ipv4Loopback, _ := net.ParseCIDR("127.0.0.0/8")
	_, ipv6Loopback, _ := net.ParseCIDR("::1/128")
	return []*net.IPNet{ipv4Loopback, ipv6Loopback}
}()

// isLoopback reports whether ip is a loopback address.
func isLoopback(ip string) bool {
	return IsTrustedProxy(ip, loopbackNets)
}

// AccessLogWriter is the interface the middleware needs from the output adapter.
// *accesslog.Writer satisfies this interface.
type AccessLogWriter interface {
	Write(entry accesslog.Entry) error
}

// AccessLogger is a middleware that writes one access-log entry per HTTP request
// to the provided writer. It runs alongside (not instead of) RequestLogger.
//
// When excludeHealthChecks is true, requests from the Gordon health prober
// (UA prefix "Gordon-HealthCheck/") or from loopback IPs are not logged.
//
// Write failures are reported as warnings through the application logger and
// never fail the HTTP response.
func AccessLogger(writer AccessLogWriter, excludeHealthChecks bool, log zerowrap.Logger, trustedNets ...[]*net.IPNet) func(http.Handler) http.Handler {
	var nets []*net.IPNet
	if len(trustedNets) > 0 {
		nets = trustedNets[0]
	}

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
			clientIP := GetClientIP(r, nets)

			next.ServeHTTP(rw, r)

			// Apply health-check exclusion after the response so we always allow
			// the request to complete, we just don't log it.
			if excludeHealthChecks {
				if strings.HasPrefix(r.UserAgent(), "Gordon-HealthCheck/") || isLoopback(clientIP) {
					return
				}
			}

			durationMS := float64(time.Since(start).Microseconds()) / 1000.0

			entry := accesslog.Entry{
				Time:       time.Now().UTC(),
				ClientIP:   clientIP,
				Method:     r.Method,
				Host:       r.Host,
				Path:       r.URL.Path,
				Query:      r.URL.RawQuery,
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
					Err(err).
					Msg("access log write failed")
			}
		})
	}
}
