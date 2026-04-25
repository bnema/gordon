package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/domain"
)

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

			if httphelper.IsTrustedProxy(clientIP, httphelper.LocalhostNets) {
				next.ServeHTTP(w, r)
				return
			}

			if httphelper.IsTrustedProxy(clientIP, allowedNets) {
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
func HTTPSRedirect(proxyNets []*net.IPNet, httpPort, tlsPort int, forceAll bool, log zerowrap.Logger, isHostAllowed func(string) bool) func(http.Handler) http.Handler {
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
				remoteIP := httphelper.ExtractRemoteIP(r.RemoteAddr)
				if httphelper.IsTrustedOrLocal(remoteIP, proxyNets) {
					next.ServeHTTP(w, r)
					return
				}
			}

			target, ok := httpsRedirectTarget(r.Host, r.RequestURI, httpPort, tlsPort, isHostAllowed)
			if !ok {
				http.Error(w, "Bad Request", http.StatusBadRequest)
				return
			}

			log.Debug().
				Str("target", target).
				Msg("redirecting HTTP client to HTTPS")

			http.Redirect(w, r, target, http.StatusPermanentRedirect)
		})
	}
}

// httpsRedirectTarget derives the HTTPS redirect URL from a validated request Host.
func httpsRedirectTarget(host, requestURI string, httpPort, tlsPort int, isHostAllowed func(string) bool) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", false
	}
	hostname, portStr, err := net.SplitHostPort(host)
	if err != nil {
		if strings.Contains(host, ":") {
			return "", false
		}
		hostname = host
	} else if port, err := strconv.Atoi(portStr); err != nil || port < 1 || port > 65535 {
		return "", false
	}
	hostname = strings.TrimSuffix(hostname, ".")
	canonicalHost, ok := domain.CanonicalRouteDomain(hostname)
	if !ok {
		return "", false
	}
	if isHostAllowed == nil || !isHostAllowed(canonicalHost) {
		return "", false
	}

	path := requestURI
	if !strings.HasPrefix(path, "/") {
		path = "/"
	}

	canonicalAuthority := canonicalHost
	if portStr != "" {
		canonicalAuthority = net.JoinHostPort(canonicalHost, portStr)
	}
	return fmt.Sprintf("https://%s%s", httphelper.HTTPSAuthority(canonicalAuthority, httpPort, tlsPort), path), true
}

// ProxyCIDRAllowlist returns middleware that restricts proxy access to the given CIDR ranges.
// This validates the direct network peer (RemoteAddr), not forwarded IPs, to ensure only
// trusted sources (e.g. Cloudflare edge IPs) can reach the proxy server.
// An empty allowedNets slice is a no-op (all traffic passes through).
func ProxyCIDRAllowlist(allowedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	return cidrAllowlist(allowedNets, func(r *http.Request) string {
		return httphelper.ExtractRemoteIP(r.RemoteAddr)
	}, "proxy origin", log)
}
