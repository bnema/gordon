package middleware

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/in"
	"gordon/internal/domain"
)

// RegistryAuth middleware provides Docker Registry authentication.
func RegistryAuth(username, password string, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Require auth for all registry operations
			if !isAuthenticated(r, username, password, log) {
				w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
				w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)

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
			Str("provided_username", username).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("authentication failed")
	} else {
		log.Debug().
			Str("username", username).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("authentication successful")
	}

	return authenticated
}

// RegistryAuthV2 middleware provides enhanced Docker Registry authentication
// supporting both password and token-based authentication.
func RegistryAuthV2(authSvc in.AuthService, log zerowrap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			switch authSvc.GetAuthType() {
			case domain.AuthTypePassword:
				authenticated = authenticatePassword(ctx, r, authSvc, log)
			case domain.AuthTypeToken:
				authenticated = authenticateToken(ctx, r, authSvc, log)
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
		Str("provided_username", username).
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Msg("password authentication failed")
	return false
}

// authenticateToken handles token-based authentication.
// It supports both Bearer token in Authorization header and token-as-password for CI.
func authenticateToken(ctx context.Context, r *http.Request, authSvc in.AuthService, log zerowrap.Logger) bool {
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
			return false
		}

		log.Debug().
			Str("subject", claims.Subject).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("bearer token authentication successful")
		return true
	}

	// Fall back to token-as-password (for CI/automation)
	// Username is the subject, password is the JWT token
	username, password, ok := r.BasicAuth()
	if !ok {
		log.Debug().
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("no auth credentials provided")
		return false
	}

	// Try to validate the password as a JWT token
	claims, err := authSvc.ValidateToken(ctx, password)
	if err != nil {
		log.Debug().
			Err(err).
			Str("provided_username", username).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("token-as-password validation failed")
		return false
	}

	// Verify the username matches the token subject
	if claims.Subject != username {
		log.Debug().
			Str("provided_username", username).
			Str("token_subject", claims.Subject).
			Str(zerowrap.FieldMethod, r.Method).
			Str(zerowrap.FieldPath, r.URL.Path).
			Msg("username does not match token subject")
		return false
	}

	log.Debug().
		Str("subject", claims.Subject).
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Msg("token-as-password authentication successful")
	return true
}

// sendUnauthorized sends an HTTP 401 response with appropriate headers.
func sendUnauthorized(w http.ResponseWriter, authType domain.AuthType, host string, log zerowrap.Logger, r *http.Request) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")

	switch authType {
	case domain.AuthTypeToken:
		// For token auth, indicate the token server endpoint
		realm := "https://" + host + "/v2/token"
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="gordon-registry"`)
	default:
		w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
	}

	http.Error(w, "Unauthorized", http.StatusUnauthorized)

	log.Warn().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "http").
		Str(zerowrap.FieldMethod, r.Method).
		Str(zerowrap.FieldPath, r.URL.Path).
		Str(zerowrap.FieldClientIP, r.RemoteAddr).
		Msg("unauthorized registry access attempt")
}
