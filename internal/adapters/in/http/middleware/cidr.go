package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
)

// localhostNets contains IPv4 and IPv6 loopback ranges that are always allowed.
var localhostNets = httphelper.ParseTrustedProxies([]string{"127.0.0.0/8", "::1"})

// cidrAllowlist is the shared implementation for CIDR-based access control middleware.
// ipExtractor determines how the client IP is obtained from the request.
// logLabel is used in the deny log message (e.g. "registry", "proxy origin").
func cidrAllowlist(allowedNets []*net.IPNet, ipExtractor func(*http.Request) string, logLabel string, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if len(allowedNets) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := ipExtractor(r)

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
				Str(zerowrap.FieldClientIP, clientIP).
				Msgf("%s access denied by CIDR allowlist", logLabel)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Forbidden"})
		})
	}
}

// ExtractRemoteIP returns the IP portion of a RemoteAddr, stripping the port.
//
// Deprecated: use httphelper.ExtractRemoteIP directly. This wrapper preserves
// the existing middleware API for callers that have not migrated yet.
func ExtractRemoteIP(remoteAddr string) string {
	return httphelper.ExtractRemoteIP(remoteAddr)
}

// IsTrustedOrLocal reports whether an IP belongs to localhost or trusted proxy nets.
//
// Deprecated: use httphelper.IsTrustedOrLocal directly. This wrapper preserves
// the existing middleware API for callers that have not migrated yet.
func IsTrustedOrLocal(ip string, proxyNets []*net.IPNet) bool {
	return httphelper.IsTrustedOrLocal(ip, proxyNets)
}

// HTTPSAuthority converts an incoming Host header plus httpPort/tlsPort into the
// correct HTTPS authority (host or host:port).
//
// Deprecated: use httphelper.HTTPSAuthority directly. This wrapper preserves
// the existing middleware API for callers that have not migrated yet.
func HTTPSAuthority(host string, httpPort, tlsPort int) string {
	return httphelper.HTTPSAuthority(host, httpPort, tlsPort)
}

// RegistryCIDRAllowlist returns middleware that restricts access to the given CIDR ranges.
// Localhost is always allowed so Gordon can pull from its own registry.
// An empty allowedNets slice is a no-op (all traffic passes through).
func RegistryCIDRAllowlist(allowedNets, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	return cidrAllowlist(allowedNets, func(r *http.Request) string {
		return GetClientIP(r, trustedNets)
	}, "registry", log)
}

// HTTPSRedirect redirects HTTP clients to the HTTPS port.
//
// When forceAll is true, all HTTP requests are redirected (for setups with no proxy).
// Otherwise, only non-trusted clients are redirected — trusted proxy IPs and localhost
// pass through since they deliver Cloudflare-proxied traffic.
// When tlsPort is 0, this is always a no-op.
//
// httpPort is the configured HTTP listener port (cfg.Server.Port) so the redirect
// helper can map it to tlsPort. Hosts with no explicit port omit the TLS port from
// the public URL; hosts with an unknown explicit port preserve it as-is.
func HTTPSRedirect(proxyNets []*net.IPNet, httpPort, tlsPort int, forceAll bool, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if tlsPort == 0 {
			return next
		}
		if !forceAll && len(proxyNets) == 0 {
			log.Info().
				Str(zerowrap.FieldLayer, "adapter").
				Str(zerowrap.FieldAdapter, "http").
				Msg("HTTP→HTTPS redirect disabled: proxy_allowed_ips is empty and force_https_redirect is false; set either to enable redirects")
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !forceAll {
				remoteIP := ExtractRemoteIP(r.RemoteAddr)
				if IsTrustedOrLocal(remoteIP, proxyNets) {
					next.ServeHTTP(w, r)
					return
				}
			}

			target := httpsRedirectTarget(r.Host, r.RequestURI, httpPort, tlsPort)

			log.Debug().
				Str("target", target).
				Msg("redirecting HTTP client to HTTPS")

			http.Redirect(w, r, target, http.StatusPermanentRedirect)
		})
	}
}

// httpsRedirectTarget derives the HTTPS redirect URL from the request Host.
func httpsRedirectTarget(host, requestURI string, httpPort, tlsPort int) string {
	path := requestURI
	if !strings.HasPrefix(path, "/") {
		path = "/"
	}

	return fmt.Sprintf("https://%s%s", HTTPSAuthority(host, httpPort, tlsPort), path)
}

// ProxyCIDRAllowlist returns middleware that restricts proxy access to the given CIDR ranges.
// This validates the direct network peer (RemoteAddr), not forwarded IPs, to ensure only
// trusted sources (e.g. Cloudflare edge IPs) can reach the proxy server.
// An empty allowedNets slice is a no-op (all traffic passes through).
func ProxyCIDRAllowlist(allowedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	return cidrAllowlist(allowedNets, func(r *http.Request) string {
		return ExtractRemoteIP(r.RemoteAddr)
	}, "proxy origin", log)
}
