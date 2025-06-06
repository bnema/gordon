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
			// Skip auth for registry info endpoint
			if r.URL.Path == "/v2/" && r.Method == "GET" {
				next.ServeHTTP(w, r)
				return
			}
			
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
	username, password, ok := r.BasicAuth()
	if !ok {
		return false
	}
	
	// Use constant-time comparison to prevent timing attacks
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(expectedUsername)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(expectedPassword)) == 1
	
	return usernameMatch && passwordMatch
}