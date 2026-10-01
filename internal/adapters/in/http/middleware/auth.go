package middleware

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/httphelper"
	"github.com/bnema/gordon/internal/adapters/in/http/registry/route"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

// TokenClaimsKey is context key for storing token claims.
// Using domain key for consistency across all auth flows.
const TokenClaimsKey = domain.TokenClaimsKey

// sanitizeHeaderValue removes characters that could enable header injection.
// Only allows alphanumeric, dots, hyphens, colons, and square brackets
// (sufficient for host:port and IPv6 addresses).
func sanitizeHeaderValue(value string) string {
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '.' || r == '-' || r == ':' || r == '[' || r == ']' {
			b.WriteRune(r)
		}
	}
	return b.String()
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
func RegistryAuthV2(authSvc in.AuthService, internalAuth InternalRegistryAuth, trustedNets []*net.IPNet, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Allow localhost requests only with internal instance credentials.
			if httphelper.IsLocalhostRequest(r) && isInternalRegistryAuth(r, internalAuth) {
				log.Debug().
					Str(zerowrap.FieldLayer, "adapter").
					Str(zerowrap.FieldAdapter, "http").
					Str(zerowrap.FieldMethod, r.Method).
					Str(zerowrap.FieldPath, r.URL.Path).
					Str(zerowrap.FieldClientIP, GetClientIP(r, trustedNets)).
					Msg("localhost request with internal auth - skipping auth")
				next.ServeHTTP(w, r)
				return
			}

			// Warn if not using TLS (skip for localhost — internal proxy traffic)
			if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" && !httphelper.IsLocalhostRequest(r) {
				log.Warn().
					Str(zerowrap.FieldLayer, "adapter").
					Str(zerowrap.FieldAdapter, "http").
					Str(zerowrap.FieldMethod, r.Method).
					Str(zerowrap.FieldPath, r.URL.Path).
					Str(zerowrap.FieldClientIP, GetClientIP(r, trustedNets)).
					Msg("registry auth over insecure HTTP connection")
			}

			// Check if auth is enabled
			if !authSvc.IsEnabled() {
				next.ServeHTTP(w, r)
				return
			}

			ctx := r.Context()

			// Authenticate based on auth type
			// When auth type is "password", accept BOTH password and token auth
			// (allows CI/CD tokens while still supporting interactive password login)
			tokenClaims, authErr := authenticateToken(ctx, r, authSvc, log)

			if authErr != nil {
				if errors.Is(authErr, domain.ErrLongLivedToken) {
					sendUnauthorizedMsg(w, authSvc.GetAuthType(), r.Host, log, trustedNets, r, domain.ErrLongLivedToken.Error())
				} else {
					sendUnauthorized(w, authSvc.GetAuthType(), r.Host, log, trustedNets, r)
				}
				return
			}

			// SECURITY: Check scopes for token auth (per-repo access control)
			if tokenClaims != nil {
				if !checkScopeAccess(r, tokenClaims, log) {
					sendForbidden(w, log, trustedNets, r)
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

// authenticateToken handles token-based authentication.
// It supports both Bearer token in Authorization header and token-as-password for CI.
// Returns (tokenClaims, error). A nil error means authentication succeeded.
// Returns domain.ErrLongLivedToken when a stored (non-ephemeral) token is used.
func authenticateToken(ctx context.Context, r *http.Request, authSvc in.AuthService, log zerowrap.Logger) (*domain.TokenClaims, error) {
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
			return nil, err
		}

		if !claims.IsEphemeral {
			log.Warn().
				Str("subject", claims.Subject).
				Str(zerowrap.FieldMethod, r.Method).
				Str(zerowrap.FieldPath, r.URL.Path).
				Msg("long-lived token rejected on registry endpoint")
			return nil, domain.ErrLongLivedToken
		}

		log.Debug().
			Str("subject", claims.Subject).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("bearer token authentication successful")
		return claims, nil
	}

	// Fall back to token-as-password (for CI/automation)
	// Username is the subject, password is the JWT token
	username, password, ok := r.BasicAuth()
	if !ok {
		log.Debug().
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("no auth credentials provided")
		return nil, errors.New("no auth credentials provided")
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
		return nil, err
	}

	// Verify the username matches the token subject
	if claims.Subject != username {
		log.Debug().
			Str("provided_username", redactUsername(username)).
			Str("token_subject", redactUsername(claims.Subject)).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("username does not match token subject")
		return nil, errors.New("username does not match token subject")
	}

	if !claims.IsEphemeral {
		log.Warn().
			Str("subject", claims.Subject).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("long-lived token rejected on registry endpoint")
		return nil, domain.ErrLongLivedToken
	}

	log.Debug().
		Str("subject", claims.Subject).
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Msg("token-as-password authentication successful")
	return claims, nil
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

// actionFromMethod returns the registry action for an HTTP method.
func actionFromMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead:
		return domain.ScopeActionPull
	case http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete:
		return domain.ScopeActionPush
	default:
		return domain.ScopeActionPull
	}
}

// checkScopeAccess verifies the token has permission for the requested operation.
// It parses the path with the same parser the registry handler dispatches
// on, so authorization and dispatch always name the same repository; a path
// the handler would reject is denied rather than allowed.
func checkScopeAccess(r *http.Request, claims *domain.TokenClaims, log zerowrap.Logger) bool {
	action := actionFromMethod(r.Method)

	op, err := route.Parse(r.URL.Path)
	if err != nil {
		log.Debug().Err(err).Str("path", r.URL.Path).Msg("malformed registry path denied")
		return false
	}
	if !op.RequiresRepositoryAuth() {
		return true
	}

	// Delegate matching to domain layer
	if domain.ScopesGrantRegistryAccess(claims.Scopes, op.Repository, action) {
		log.Debug().
			Str("repo", op.Repository).
			Str("action", action).
			Strs("scopes", claims.Scopes).
			Msg("scope access granted")
		return true
	}

	log.Debug().
		Str("repo", op.Repository).
		Str("action", action).
		Strs("scopes", claims.Scopes).
		Msg("no scope grants access")
	return false
}

// sendForbidden sends an HTTP 403 response.
func sendForbidden(w http.ResponseWriter, log zerowrap.Logger, trustedNets []*net.IPNet, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "Forbidden: insufficient scope"})

	log.Warn().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "http").
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Str(zerowrap.FieldClientIP, GetClientIP(r, trustedNets)).
		Msg("forbidden: insufficient scope for operation")
}

// sendUnauthorizedMsg sends an HTTP 401 response with a custom error message.
func sendUnauthorizedMsg(w http.ResponseWriter, authType domain.AuthType, host string, log zerowrap.Logger, trustedNets []*net.IPNet, r *http.Request, msg string) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")

	switch authType {
	case domain.AuthTypeToken:
		realmHost := host
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		realmHost = sanitizeHeaderValue(realmHost)
		realm := scheme + "://" + realmHost + "/auth/token"
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="gordon-registry"`)
	default:
		w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: msg})

	log.Warn().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "http").
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Str(zerowrap.FieldClientIP, GetClientIP(r, trustedNets)).
		Msg("unauthorized registry access attempt")
}

// sendUnauthorized sends an HTTP 401 response with appropriate headers.
func sendUnauthorized(w http.ResponseWriter, authType domain.AuthType, host string, log zerowrap.Logger, trustedNets []*net.IPNet, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")

	switch authType {
	case domain.AuthTypeToken:
		// For token auth, indicate the token server endpoint.
		// SECURITY: Only use X-Forwarded-Host/Proto from trusted sources.
		// The Host header from the request is used as the default.
		// X-Forwarded-Host is NOT trusted here because this middleware doesn't
		// have access to trusted proxy configuration, and an attacker could
		// inject a malicious realm URL to phish for credentials.
		realmHost := host

		// Detect scheme from TLS state only (not from headers that can be spoofed)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}

		// SECURITY: Sanitize the host to prevent header injection.
		// Remove any characters that could break the WWW-Authenticate header format.
		realmHost = sanitizeHeaderValue(realmHost)

		realm := scheme + "://" + realmHost + "/auth/token"
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
		Str(zerowrap.FieldClientIP, GetClientIP(r, trustedNets)).
		Msg("unauthorized registry access attempt")
}
