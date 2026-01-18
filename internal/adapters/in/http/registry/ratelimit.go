// Package registry implements the HTTP adapter for the registry API.
package registry

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/adapters/dto"
	"gordon/internal/boundaries/out"
)

// RateLimitMiddleware creates rate limiting middleware for the registry API.
// It uses the provided RateLimiter interfaces for global and per-IP limits.
// IP extraction (trusted proxy handling) remains in this HTTP adapter.
func RateLimitMiddleware(
	globalLimiter out.RateLimiter,
	ipLimiter out.RateLimiter,
	trustedProxies []string,
	log zerowrap.Logger,
) func(http.Handler) http.Handler {
	// If either limiter is nil, pass through without rate limiting
	if globalLimiter == nil || ipLimiter == nil {
		return func(next http.Handler) http.Handler {
			return next
		}
	}

	// Parse trusted proxy CIDRs once at middleware creation
	trustedNets := parseTrustedProxies(trustedProxies)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Check global rate limit
			if !globalLimiter.Allow(ctx, "global") {
				sendRateLimitError(w)
				return
			}

			// Check per-IP rate limit
			ip := getClientIP(r, trustedNets)
			if !ipLimiter.Allow(ctx, "ip:"+ip) {
				sendRateLimitError(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// parseTrustedProxies converts a list of IP addresses and CIDR ranges to net.IPNet.
func parseTrustedProxies(proxies []string) []*net.IPNet {
	var nets []*net.IPNet
	for _, proxy := range proxies {
		// Try parsing as CIDR
		_, ipNet, err := net.ParseCIDR(proxy)
		if err == nil {
			nets = append(nets, ipNet)
			continue
		}
		// Try parsing as single IP
		ip := net.ParseIP(proxy)
		if ip != nil {
			// Convert single IP to /32 or /128 CIDR
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
		}
	}
	return nets
}

// isTrustedProxy checks if the given IP is from a trusted proxy.
func isTrustedProxy(ip string, trustedNets []*net.IPNet) bool {
	if len(trustedNets) == 0 {
		return false
	}
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}
	for _, ipNet := range trustedNets {
		if ipNet.Contains(parsedIP) {
			return true
		}
	}
	return false
}

// sendRateLimitError sends an HTTP 429 response in Docker Registry API format.
func sendRateLimitError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusTooManyRequests)
	_ = json.NewEncoder(w).Encode(dto.RegistryErrorResponse{
		Errors: []dto.RegistryErrorItem{{
			Code:    "TOOMANYREQUESTS",
			Message: "rate limit exceeded",
		}},
	})
}

// getClientIP extracts the client IP address from the request.
// It only honors X-Forwarded-For and X-Real-IP headers when the request
// originates from a trusted proxy. This prevents attackers from spoofing
// their IP address when Gordon is exposed directly without a reverse proxy.
func getClientIP(r *http.Request, trustedNets []*net.IPNet) string {
	// Extract the direct connection IP from RemoteAddr
	remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remoteIP = r.RemoteAddr
	}

	// Only honor proxy headers if the request comes from a trusted proxy
	if isTrustedProxy(remoteIP, trustedNets) {
		// Check X-Forwarded-For for proxied requests
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Take first IP in chain (original client)
			if first, _, found := strings.Cut(xff, ","); found {
				return strings.TrimSpace(first)
			}
			return strings.TrimSpace(xff)
		}

		// Check X-Real-IP
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}

	// Fall back to RemoteAddr (direct connection IP)
	return remoteIP
}
