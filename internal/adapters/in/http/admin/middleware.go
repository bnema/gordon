package admin

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/middleware"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// AuthMiddleware creates middleware that validates admin API authentication.
// Both globalLimiter and ipLimiter can be nil to disable rate limiting.
// The trustedNets parameter is used for proper IP extraction behind reverse proxies.
func AuthMiddleware(
	authSvc in.AuthService,
	globalLimiter out.RateLimiter,
	ipLimiter out.RateLimiter,
	trustedNets []*net.IPNet,
	log zerowrap.Logger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Check global rate limit
			if globalLimiter != nil && !globalLimiter.Allow(ctx, "global") {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "rate limit exceeded"})
				return
			}

			// Check per-IP rate limit
			if ipLimiter != nil {
				ip := middleware.GetClientIP(r, trustedNets)
				if !ipLimiter.Allow(ctx, "ip:"+ip) {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "1")
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "rate limit exceeded"})
					return
				}
			}

			// Check if auth is enabled.
			// DESIGN NOTE: When auth is disabled, the admin API is intentionally
			// accessible without credentials. This is the expected behavior for
			// local/development setups where Gordon is not exposed to the internet.
			// Users who disable auth accept this tradeoff explicitly in their config.
			// For production deployments, auth should always be enabled.
			if !authSvc.IsEnabled() {
				log.Warn().
					Str("path", r.URL.Path).
					Str("method", r.Method).
					Str("remote_addr", r.RemoteAddr).
					Msg("auth disabled - allowing unauthenticated admin API access (intended for local use only)")
				next.ServeHTTP(w, r)
				return
			}

			// Extract token from Authorization header.
			// SECURITY: Require the standard "Bearer" prefix per RFC 6750
			// to prevent accidental token exposure in non-standard formats.
			auth := r.Header.Get("Authorization")
			if auth == "" {
				sendUnauthorized(w, "missing authorization header")
				return
			}

			if !strings.HasPrefix(auth, "Bearer ") {
				sendUnauthorized(w, "authorization header must use Bearer scheme")
				return
			}
			token := strings.TrimPrefix(auth, "Bearer ")

			// Validate token
			claims, err := authSvc.ValidateToken(ctx, token)
			if err != nil {
				log.Warn().Err(err).Msg("invalid admin token")
				sendUnauthorized(w, "invalid token")
				return
			}

			// Check if token has admin scopes
			hasAdminScope := false
			for _, scope := range claims.Scopes {
				if strings.HasPrefix(scope, domain.ScopeTypeAdmin+":") {
					hasAdminScope = true
					break
				}
			}

			if !hasAdminScope {
				log.Warn().Str("subject", claims.Subject).Msg("token missing admin scopes")
				sendForbidden(w, "admin scope required")
				return
			}

			// Add claims to context for downstream handlers
			ctx = context.WithValue(ctx, domain.ContextKeyScopes, claims.Scopes)
			ctx = context.WithValue(ctx, domain.ContextKeySubject, claims.Subject)
			ctx = context.WithValue(ctx, domain.TokenClaimsKey, claims)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireScope creates middleware that checks for a specific admin scope.
func RequireScope(resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Get scopes from context
			scopes, ok := ctx.Value(domain.ContextKeyScopes).([]string)
			if !ok {
				sendForbidden(w, "no scopes in context")
				return
			}

			// Check if user has required scope
			if !domain.HasAdminAccess(scopes, resource, action) {
				sendForbidden(w, "insufficient permissions")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// GetSubject retrieves the authenticated subject from the context.
func GetSubject(ctx context.Context) string {
	subject, _ := ctx.Value(domain.ContextKeySubject).(string)
	return subject
}

// GetScopes retrieves the token scopes from the context.
func GetScopes(ctx context.Context) []string {
	scopes, _ := ctx.Value(domain.ContextKeyScopes).([]string)
	return scopes
}

// HasAccess checks if the context has access to the given resource and action.
func HasAccess(ctx context.Context, resource, action string) bool {
	scopes := GetScopes(ctx)
	return domain.HasAdminAccess(scopes, resource, action)
}

func sendUnauthorized(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("WWW-Authenticate", `Bearer realm="gordon-admin"`)
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: message})
}

func sendForbidden(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: message})
}
