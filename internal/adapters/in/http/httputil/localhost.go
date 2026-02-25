package httputil

import (
	"net"
	"net/http"
	"net/netip"
)

// IsLocalhostRequest reports whether the request originates from localhost.
// SECURITY: Uses RemoteAddr (server-set) instead of Host header (client-spoofable).
func IsLocalhostRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr has no port (unusual but possible in tests/Unix sockets).
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.IsLoopback()
}
