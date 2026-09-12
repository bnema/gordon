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
	return HTTPSRedirectWithEligibility(proxyNets, httpPort, tlsPort, forceAll, log, isHostAllowed, nil, nil)
}

// HTTPSRedirectWithEligibility is HTTPSRedirect with an extra opt-out and
// an enforced-TLS predicate:
//   - isRedirectEligible reports whether a KNOWN host should actually be
//     redirected (e.g. app hosts declared tls=never stay plain HTTP).
//     A known but ineligible host passes through to next instead of
//     redirecting; unknown/invalid hosts still get 400. Nil behaves like
//     HTTPSRedirect (every allowed host is eligible).
//   - isTLSAlways reports whether a host requires HTTPS unconditionally
//     (tls=always). Such a host is redirected when an HTTPS endpoint
//     exists and otherwise refused, never served over plaintext. Nil
//     disables the check.
func HTTPSRedirectWithEligibility(proxyNets []*net.IPNet, httpPort, tlsPort int, forceAll bool, log zerowrap.Logger, isHostAllowed func(string) bool, isRedirectEligible func(string) bool, isTLSAlways func(string) bool) func(http.Handler) http.Handler {
	if tlsPort == 0 && isTLSAlways == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	if !forceAll && len(proxyNets) == 0 && isTLSAlways == nil {
		log.Info().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "http").
			Msg("HTTP→HTTPS redirect disabled: proxy_allowed_ips is empty and force_https_redirect is false; set either to enable redirects")
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A tls=always host must never be served over plaintext. It is
			// redirected when an HTTPS endpoint exists and refused when it
			// does not, regardless of proxy/redirect configuration.
			if isTLSAlways != nil && isTLSAlwaysForHost(r.Host, isTLSAlways) {
				handleTLSAlways(w, r, httpPort, tlsPort, isHostAllowed, log)
				return
			}

			if tlsPort == 0 {
				next.ServeHTTP(w, r)
				return
			}
			if !forceAll {
				remoteIP := httphelper.ExtractRemoteIP(r.RemoteAddr)
				if httphelper.IsTrustedOrLocal(remoteIP, proxyNets) {
					next.ServeHTTP(w, r)
					return
				}
			}

			target, ok := httpsRedirectTarget(r.Host, r.RequestURI, httpPort, tlsPort, isHostAllowed)
			if ok && isRedirectEligible != nil && !isRedirectEligibleForHost(r.Host, isRedirectEligible) {
				// Known host that opted out of TLS (e.g. tls=never):
				// serve plain HTTP through the normal chain.
				next.ServeHTTP(w, r)
				return
			}
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

// handleTLSAlways redirects or refuses a request to a tls=always host. It
// never forwards: the backend is not reached over plaintext.
func handleTLSAlways(w http.ResponseWriter, r *http.Request, httpPort, tlsPort int, isHostAllowed func(string) bool, log zerowrap.Logger) {
	if tlsPort == 0 {
		log.Warn().Str("host", r.Host).Msg("plaintext refused: host requires TLS but no HTTPS endpoint is configured")
		http.Error(w, "Misdirected Request", http.StatusMisdirectedRequest)
		return
	}
	target, ok := httpsRedirectTarget(r.Host, r.RequestURI, httpPort, tlsPort, isHostAllowed)
	if !ok {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, target, http.StatusPermanentRedirect)
}

// isTLSAlwaysForHost reports whether the canonical request Host requires
// HTTPS. Invalid hosts are never treated as always.
func isTLSAlwaysForHost(host string, isTLSAlways func(string) bool) bool {
	canonical, ok := canonicalRedirectHost(host)
	if !ok {
		return false
	}
	return isTLSAlways(canonical)
}

// isRedirectEligibleForHost reports whether the canonical request Host
// is eligible for redirect. Invalid hosts are never eligible.
func isRedirectEligibleForHost(host string, isRedirectEligible func(string) bool) bool {
	canonical, ok := canonicalRedirectHost(host)
	if !ok {
		return false
	}
	return isRedirectEligible(canonical)
}

// canonicalRedirectHost lowercases, strips ports/trailing dots and
// validates the request Host, returning the canonical route domain.
func canonicalRedirectHost(host string) (string, bool) {
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
	return domain.CanonicalRouteDomain(hostname)
}

// httpsRedirectTarget derives the HTTPS redirect URL from a validated request Host.
func httpsRedirectTarget(host, requestURI string, httpPort, tlsPort int, isHostAllowed func(string) bool) (string, bool) {
	raw := strings.ToLower(strings.TrimSpace(host))
	_, portStr, splitErr := net.SplitHostPort(raw)
	hasPort := splitErr == nil
	canonicalHost, ok := canonicalRedirectHost(host)
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
	if hasPort {
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
