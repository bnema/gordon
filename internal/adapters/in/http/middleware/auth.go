package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/adapters/dto"
	"gordon/internal/boundaries/in"
	"gordon/internal/domain"
)

// contextKey is a type for context keys used by this package.
type contextKey string

// TokenClaimsKey is the context key for storing token claims.
const TokenClaimsKey contextKey = "tokenClaims"

// RegistryAuth middleware provides Docker Registry authentication.
func RegistryAuth(username, password string, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Require auth for all registry operations
			if !isAuthenticated(r, username, password, log) {
				w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
				w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Unauthorized"})

				log.Warn().
					Str(zerowrap.FieldLayer, "adapter").
					Str(zerowrap.FieldAdapter, "http").
					Str(zerowrap.FieldMethod, r.Method).
					Str(zerowrap.FieldPath, r.URL.Path).
					Str(zerowrap.FieldClientIP, r.RemoteAddr).
					Msg("unauthorized registry access attempt")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isAuthenticated checks basic auth credentials.
func isAuthenticated(r *http.Request, expectedUsername, expectedPassword string, log zerowrap.Logger) bool {
	authHeader := r.Header.Get("Authorization")
	log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "http").
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Bool("has_auth_header", authHeader != "").
		Msg("processing authentication request")

	username, password, ok := r.BasicAuth()
	if !ok {
		log.Debug().
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("no basic auth provided")
		return false
	}

	// Use constant-time comparison to prevent timing attacks
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(expectedUsername)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(expectedPassword)) == 1

	authenticated := usernameMatch && passwordMatch
	if !authenticated {
		log.Debug().
			Str("provided_username", redactUsername(username)).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("authentication failed")
	} else {
		log.Debug().
			Str("username", redactUsername(username)).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("authentication successful")
	}

	return authenticated
}

// redactUsername partially redacts a username for logging.
// Shows first and last character with asterisks in between.
// SECURITY: Prevents full username exposure in logs.
func redactUsername(username string) string {
	if len(username) <= 2 {
		return "***"
	}
	return username[:1] + "***" + username[len(username)-1:]
}

// InternalRegistryAuth holds the credentials used for loopback-only registry access.
// These are generated per Gordon instance and are never exposed in config.
type InternalRegistryAuth struct {
	Username string
	Password string
}

// RegistryAuthV2 middleware provides enhanced Docker Registry authentication
// supporting both password and token-based authentication.
func RegistryAuthV2(authSvc in.AuthService, internalAuth InternalRegistryAuth, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Allow localhost requests only with internal instance credentials.
			if isLocalhostRequest(r) && isInternalRegistryAuth(r, internalAuth) {
				log.Debug().
					Str(zerowrap.FieldLayer, "adapter").
					Str(zerowrap.FieldAdapter, "http").
					Str(zerowrap.FieldMethod, r.Method).
					Str(zerowrap.FieldPath, r.URL.Path).
					Str(zerowrap.FieldClientIP, r.RemoteAddr).
					Msg("localhost request with internal auth - skipping auth")
				next.ServeHTTP(w, r)
				return
			}

			// Warn if not using TLS
			if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
				log.Warn().
					Str(zerowrap.FieldLayer, "adapter").
					Str(zerowrap.FieldAdapter, "http").
					Str(zerowrap.FieldMethod, r.Method).
					Str(zerowrap.FieldPath, r.URL.Path).
					Str(zerowrap.FieldClientIP, r.RemoteAddr).
					Msg("registry auth over insecure HTTP connection")
			}

			// Check if auth is enabled
			if !authSvc.IsEnabled() {
				next.ServeHTTP(w, r)
				return
			}

			ctx := r.Context()

			// Authenticate based on auth type
			var authenticated bool
			var tokenClaims *domain.TokenClaims
			switch authSvc.GetAuthType() {
			case domain.AuthTypePassword:
				authenticated = authenticatePassword(ctx, r, authSvc, log)
			case domain.AuthTypeToken:
				authenticated, tokenClaims = authenticateToken(ctx, r, authSvc, log)
			default:
				log.Error().
					Str("auth_type", string(authSvc.GetAuthType())).
					Msg("unknown auth type")
				authenticated = false
			}

			if !authenticated {
				sendUnauthorized(w, authSvc.GetAuthType(), r.Host, log, r)
				return
			}

			// SECURITY: Check scopes for token auth (per-repo access control)
			if tokenClaims != nil {
				if !checkScopeAccess(r, tokenClaims, log) {
					sendForbidden(w, log, r)
					return
				}
				// Store claims in context for downstream handlers that need access to token metadata
				// (e.g., audit logging, rate limiting by subject, or future per-user quotas)
				ctx = context.WithValue(ctx, TokenClaimsKey, tokenClaims)
				r = r.WithContext(ctx)
			}

			next.ServeHTTP(w, r)
		})
	}
}

// authenticatePassword handles password-based authentication.
func authenticatePassword(ctx context.Context, r *http.Request, authSvc in.AuthService, log zerowrap.Logger) bool {
	username, password, ok := r.BasicAuth()
	if !ok {
		log.Debug().
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("no basic auth provided")
		return false
	}

	if authSvc.ValidatePassword(ctx, username, password) {
		log.Debug().
			Str("username", username).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("password authentication successful")
		return true
	}

	log.Debug().
		Str("provided_username", redactUsername(username)).
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Msg("password authentication failed")
	return false
}

// authenticateToken handles token-based authentication.
// It supports both Bearer token in Authorization header and token-as-password for CI.
// Returns (authenticated, tokenClaims) where tokenClaims may be nil on failure.
func authenticateToken(ctx context.Context, r *http.Request, authSvc in.AuthService, log zerowrap.Logger) (bool, *domain.TokenClaims) {
	// First, check for Bearer token
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := authSvc.ValidateToken(ctx, token)
		if err != nil {
			log.Debug().
				Err(err).
				Str(zerowrap.FieldMethod, r.Method).
				Str(zerowrap.FieldPath, r.URL.Path).
				Msg("bearer token validation failed")
			return false, nil
		}

		log.Debug().
			Str("subject", claims.Subject).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("bearer token authentication successful")
		return true, claims
	}

	// Fall back to token-as-password (for CI/automation)
	// Username is the subject, password is the JWT token
	username, password, ok := r.BasicAuth()
	if !ok {
		log.Debug().
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("no auth credentials provided")
		return false, nil
	}

	// Try to validate the password as a JWT token
	claims, err := authSvc.ValidateToken(ctx, password)
	if err != nil {
		log.Debug().
			Err(err).
			Str("provided_username", redactUsername(username)).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("token-as-password validation failed")
		return false, nil
	}

	// Verify the username matches the token subject
	if claims.Subject != username {
		log.Debug().
			Str("provided_username", redactUsername(username)).
			Str("token_subject", redactUsername(claims.Subject)).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("username does not match token subject")
		return false, nil
	}

	log.Debug().
		Str("subject", claims.Subject).
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Msg("token-as-password authentication successful")
	return true, claims
}

// isLocalhostRequest checks if the request originates from localhost.
// This is used to allow Gordon to pull from its own registry with internal auth.
func isLocalhostRequest(r *http.Request) bool {
	host := r.RemoteAddr
	// RemoteAddr includes port, e.g., "127.0.0.1:12345" or "[::1]:12345"
	if strings.HasPrefix(host, "127.") ||
		strings.HasPrefix(host, "[::1]") ||
		strings.HasPrefix(host, "::1") ||
		strings.HasPrefix(host, "localhost") {
		return true
	}
	return false
}

func isInternalRegistryAuth(r *http.Request, internalAuth InternalRegistryAuth) bool {
	if internalAuth.Username == "" || internalAuth.Password == "" {
		return false
	}

	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}

	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(internalAuth.Username)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(internalAuth.Password)) == 1
	return usernameMatch && passwordMatch
}

// checkScopeAccess verifies the token has permission for the requested operation.
// Maps HTTP method to registry action and checks if any token scope grants access.
func checkScopeAccess(r *http.Request, claims *domain.TokenClaims, log zerowrap.Logger) bool {
	// Determine required action from HTTP method
	var action string
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		action = domain.ScopeActionPull
	case http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete:
		action = domain.ScopeActionPush
	default:
		action = domain.ScopeActionPull // Default to pull for unknown methods
	}

	// Extract repository name from URL path
	// Paths are like /v2/{name}/manifests/{reference} or /v2/{name}/blobs/{digest}
	path := r.URL.Path
	if !strings.HasPrefix(path, "/v2/") {
		// Not a registry path, allow
		return true
	}

	// Remove /v2/ prefix and find repository name
	pathParts := strings.Split(strings.TrimPrefix(path, "/v2/"), "/")
	if len(pathParts) < 2 {
		// Malformed path or /v2/ root, allow
		return true
	}

	// Find the boundary between repo name and route (manifests, blobs, tags)
	var repoNameParts []string
	for i, part := range pathParts {
		if part == "manifests" || part == "blobs" || part == "tags" || part == "_catalog" {
			repoNameParts = pathParts[:i]
			break
		}
	}
	if len(repoNameParts) == 0 {
		// Could be a special route like /v2/token, allow
		return true
	}

	repoName := strings.Join(repoNameParts, "/")

	// Check if any token scope grants access
	for _, scopeStr := range claims.Scopes {
		scope, err := domain.ParseScope(scopeStr)
		if err != nil {
			log.Debug().Err(err).Str("scope", scopeStr).Msg("failed to parse scope")
			continue
		}

		if scope.CanAccess(repoName, action) {
			log.Debug().
				Str("repo", repoName).
				Str("action", action).
				Str("scope", scopeStr).
				Msg("scope access granted")
			return true
		}
	}

	log.Debug().
		Str("repo", repoName).
		Str("action", action).
		Strs("scopes", claims.Scopes).
		Msg("no scope grants access")
	return false
}

// sendForbidden sends an HTTP 403 response.
func sendForbidden(w http.ResponseWriter, log zerowrap.Logger, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Forbidden: insufficient scope"})

	log.Warn().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "http").
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Str(zerowrap.FieldClientIP, r.RemoteAddr).
		Msg("forbidden: insufficient scope for operation")
}

// sendUnauthorized sends an HTTP 401 response with appropriate headers.
func sendUnauthorized(w http.ResponseWriter, authType domain.AuthType, host string, log zerowrap.Logger, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")

	switch authType {
	case domain.AuthTypeToken:
		// For token auth, indicate the token server endpoint
		// Use X-Forwarded-Host if behind proxy, otherwise use Host header
		realmHost := r.Header.Get("X-Forwarded-Host")
		if realmHost == "" {
			realmHost = host
		}
		// Detect scheme: prioritize X-Forwarded-Proto (set by reverse proxies like Cloudflare)
		scheme := "http"
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else if r.TLS != nil {
			scheme = "https"
		}
		realm := scheme + "://" + realmHost + "/v2/token"
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="gordon-registry"`)
	default:
		w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Unauthorized"})

	log.Warn().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "http").
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Str(zerowrap.FieldClientIP, r.RemoteAddr).
		Msg("unauthorized registry access attempt")
}
