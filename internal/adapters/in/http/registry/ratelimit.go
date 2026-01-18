// Package registry implements the HTTP adapter for the registry API.
package registry

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/time/rate"

	"gordon/internal/adapters/dto"
)

// RateLimitConfig holds rate limiting configuration for the registry API.
// These settings help prevent DoS attacks and brute force attempts.
type RateLimitConfig struct {
	// Enabled controls whether rate limiting is active.
	// Default: true
	Enabled bool

	// GlobalRPS is the maximum requests per second across all clients.
	// Default: 500
	GlobalRPS float64

	// PerIPRPS is the maximum requests per second per client IP.
	// Default: 50
	PerIPRPS float64

	// Burst is the maximum burst size for rate limiters.
	// Default: 100
	Burst int

	// TrustedProxies is a list of IP addresses or CIDR ranges that are trusted
	// to set X-Forwarded-For and X-Real-IP headers. If empty, these headers are
	// ignored and RemoteAddr is always used (secure default for direct exposure).
	// Example: ["127.0.0.1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"]
	TrustedProxies []string
}

// DefaultRateLimitConfig returns sensible default rate limiting configuration.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		Enabled:   true,
		GlobalRPS: 500, // 500 requests/second globally
		PerIPRPS:  50,  // 50 requests/second per IP
		Burst:     100, // Allow burst of 100 requests
	}
}

// ipRateLimiter manages per-IP rate limiters.
type ipRateLimiter struct {
	limiters map[string]*rate.Limiter
	mu       sync.RWMutex
	rps      float64
	burst    int
}

func newIPRateLimiter(rps float64, burst int) *ipRateLimiter {
	return &ipRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rps:      rps,
		burst:    burst,
	}
}

func (l *ipRateLimiter) getLimiter(ip string) *rate.Limiter {
	l.mu.RLock()
	limiter, exists := l.limiters[ip]
	l.mu.RUnlock()

	if exists {
		return limiter
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock
	if limiter, exists = l.limiters[ip]; exists {
		return limiter
	}

	limiter = rate.NewLimiter(rate.Limit(l.rps), l.burst)
	l.limiters[ip] = limiter
	return limiter
}

// RateLimitMiddleware creates rate limiting middleware for the registry API.
// Returns nil if rate limiting is disabled.
func RateLimitMiddleware(config RateLimitConfig) func(http.Handler) http.Handler {
	if !config.Enabled {
		return func(next http.Handler) http.Handler {
			return next // Pass through if disabled
		}
	}

	globalLimiter := rate.NewLimiter(rate.Limit(config.GlobalRPS), config.Burst)
	ipLimiter := newIPRateLimiter(config.PerIPRPS, config.Burst)

	// Parse trusted proxy CIDRs once at middleware creation
	trustedNets := parseTrustedProxies(config.TrustedProxies)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check global rate limit
			if !globalLimiter.Allow() {
				sendRateLimitError(w)
				return
			}

			// Check per-IP rate limit
			ip := getClientIP(r, trustedNets)
			if !ipLimiter.getLimiter(ip).Allow() {
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
