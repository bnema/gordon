package admin

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/adapters/dto"
	"gordon/internal/adapters/in/http/middleware"
	"gordon/internal/boundaries/in"
	"gordon/internal/boundaries/out"
	"gordon/internal/domain"
)

// contextKey is a custom type for context keys to avoid collisions.
type contextKey string

const (
	// ContextKeyScopes is the context key for storing token scopes.
	ContextKeyScopes contextKey = "admin_scopes"
	// ContextKeySubject is the context key for storing token subject.
	ContextKeySubject contextKey = "admin_subject"
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

			// Check if auth is enabled
			if !authSvc.IsEnabled() {
				// Auth disabled, log warning and allow all requests
				log.Warn().
					Str("path", r.URL.Path).
					Str("method", r.Method).
					Str("remote_addr", r.RemoteAddr).
					Msg("auth disabled - allowing unauthenticated admin API access")
				next.ServeHTTP(w, r)
				return
			}

			// Extract token from Authorization header
			auth := r.Header.Get("Authorization")
			if auth == "" {
				sendUnauthorized(w, "missing authorization header")
				return
			}

			// Support both "Bearer <token>" and direct token
			token := auth
			if strings.HasPrefix(auth, "Bearer ") {
				token = strings.TrimPrefix(auth, "Bearer ")
			}

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
			ctx = context.WithValue(ctx, ContextKeyScopes, claims.Scopes)
			ctx = context.WithValue(ctx, ContextKeySubject, claims.Subject)

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
			scopes, ok := ctx.Value(ContextKeyScopes).([]string)
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
	subject, _ := ctx.Value(ContextKeySubject).(string)
	return subject
}

// GetScopes retrieves the token scopes from the context.
func GetScopes(ctx context.Context) []string {
	scopes, _ := ctx.Value(ContextKeyScopes).([]string)
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
