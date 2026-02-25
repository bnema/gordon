package httputil

import (
	"net/http"
	"strings"
)

// IsLocalhostRequest reports whether the request originates from localhost.
// SECURITY: Uses RemoteAddr (server-set) instead of Host header (client-spoofable).
// Go's net/http always formats RemoteAddr as "host:port" where IPv6 is bracketed:
// e.g., "127.0.0.1:12345" or "[::1]:12345".
func IsLocalhostRequest(r *http.Request) bool {
	host := r.RemoteAddr
	return strings.HasPrefix(host, "127.") ||
		strings.HasPrefix(host, "[::1]")
}
