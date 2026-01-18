// Package registry implements the HTTP adapter for the registry API.
package registry

import (
	"encoding/json"
	"net/http"

	"github.com/bnema/zerowrap"

	"gordon/internal/adapters/dto"
	"gordon/internal/adapters/in/http/middleware"
	"gordon/internal/boundaries/out"
)

// RateLimitMiddleware creates rate limiting middleware for the registry API.
// It uses the provided RateLimiter interfaces for global and per-IP limits.
// IP extraction uses shared middleware.GetClientIP for trusted proxy handling.
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
	trustedNets := middleware.ParseTrustedProxies(trustedProxies)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Check global rate limit
			if !globalLimiter.Allow(ctx, "global") {
				sendRateLimitError(w)
				return
			}

			// Check per-IP rate limit
			ip := middleware.GetClientIP(r, trustedNets)
			if !ipLimiter.Allow(ctx, "ip:"+ip) {
				sendRateLimitError(w)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
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
