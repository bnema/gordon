package middleware

import (
	"encoding/json"
	"net"
	"net/http"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// localhostNets contains IPv4 and IPv6 loopback ranges that are always allowed.
var localhostNets = ParseTrustedProxies([]string{"127.0.0.0/8", "::1"})

// RegistryCIDRAllowlist returns middleware that restricts access to the given CIDR ranges.
// Localhost is always allowed so Gordon can pull from its own registry.
// An empty allowedNets slice is a no-op (all traffic passes through).
func RegistryCIDRAllowlist(allowedNets, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(allowedNets) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := GetClientIP(r, trustedNets)

			// Always allow localhost — Gordon must reach its own registry.
			if IsTrustedProxy(clientIP, localhostNets) {
				next.ServeHTTP(w, r)
				return
			}

			if IsTrustedProxy(clientIP, allowedNets) {
				next.ServeHTTP(w, r)
				return
			}

			log.Warn().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "http").
				Str(zerowrap.FieldMethod, r.Method).
				Str(zerowrap.FieldPath, r.URL.Path).
				Str(zerowrap.FieldClientIP, clientIP).
				Msg("registry access denied by CIDR allowlist")

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Forbidden"})
		})
	}
}

// ProxyCIDRAllowlist returns middleware that restricts proxy access to the given CIDR ranges.
// This is used to ensure only trusted sources (e.g. Cloudflare edge IPs) can reach the
// proxy server, preventing direct-to-origin attacks that bypass CDN protections.
// An empty allowedNets slice is a no-op (all traffic passes through).
func ProxyCIDRAllowlist(allowedNets, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(allowedNets) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// For proxy allowlisting we check the direct connection IP (RemoteAddr),
			// not the client IP from headers, because we're validating the network
			// peer (e.g. Cloudflare edge), not the end-user.
			remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				remoteIP = r.RemoteAddr
			}

			if IsTrustedProxy(remoteIP, localhostNets) {
				next.ServeHTTP(w, r)
				return
			}

			if IsTrustedProxy(remoteIP, allowedNets) {
				next.ServeHTTP(w, r)
				return
			}

			log.Warn().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "http").
				Str(zerowrap.FieldMethod, r.Method).
				Str("host", r.Host).
				Str(zerowrap.FieldClientIP, remoteIP).
				Msg("proxy access denied by origin IP allowlist")

			http.Error(w, "Forbidden", http.StatusForbidden)
		})
	}
}
