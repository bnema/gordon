package httphelper

import (
	"net"
	"net/http"
	"net/netip"
)

// IsLocalhostRequest reports whether the request originates from localhost.
// SECURITY: Uses RemoteAddr (server-set) instead of Host header (client-spoofable).
func IsLocalhostRequest(r *http.Request) bool {
	addr, ok := remoteAddr(r)
	return ok && addr.IsLoopback()
}

func remoteAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	return addr, err == nil
}
