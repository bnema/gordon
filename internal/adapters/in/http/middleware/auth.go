package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/bnema/zerowrap"
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
