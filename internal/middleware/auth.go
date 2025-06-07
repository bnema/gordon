package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/rs/zerolog/log"
)

// RegistryAuth middleware provides Docker Registry authentication (optional)
func RegistryAuth(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Require auth for all other registry operations
			if !isAuthenticated(r, username, password) {
				w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
				w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)

				log.Warn().
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("remote_addr", r.RemoteAddr).
					Msg("Unauthorized registry access attempt")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// Helper functions

func isAuthenticated(r *http.Request, expectedUsername, expectedPassword string) bool {
	// Debug: Log raw Authorization header
	authHeader := r.Header.Get("Authorization")
	log.Debug().
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Str("authorization_header", authHeader).
		Msg("Processing authentication request")

	username, password, ok := r.BasicAuth()
	if !ok {
		log.Debug().
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("authorization_header", authHeader).
			Msg("No basic auth provided")
		return false
	}

	// Use constant-time comparison to prevent timing attacks
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(expectedUsername)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(expectedPassword)) == 1

	authenticated := usernameMatch && passwordMatch
	if !authenticated {
		log.Debug().
			Str("provided_username", username).
			Str("expected_username", expectedUsername).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Str("authorization_header", authHeader).
			Msg("Authentication failed")
	} else {
		log.Debug().
			Str("provided_username", username).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Msg("Authentication successful")
	}

	return authenticated
}
