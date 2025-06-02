package middleware

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

// BasicAuth middleware provides HTTP Basic Authentication
func BasicAuth(username, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isAuthenticated(r, username, password) {
				w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Management API"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				
				log.Warn().
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("remote_addr", r.RemoteAddr).
					Msg("Unauthorized API access attempt")
				return
			}
			
			next.ServeHTTP(w, r)
		})
	}
}

// APIKeyAuth middleware provides API key authentication
func APIKeyAuth(validAPIKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := r.Header.Get("X-API-Key")
			if apiKey == "" {
				apiKey = r.URL.Query().Get("api_key")
			}
			
			if !isValidAPIKey(apiKey, validAPIKey) {
				http.Error(w, "Forbidden - Invalid API Key", http.StatusForbidden)
				
				log.Warn().
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("remote_addr", r.RemoteAddr).
					Msg("Invalid API key access attempt")
				return
			}
			
			next.ServeHTTP(w, r)
		})
	}
}

// RegistryAuth middleware provides Docker Registry authentication
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

// IPWhitelist middleware restricts access to specific IP addresses
func IPWhitelist(allowedIPs []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := getClientIP(r)
			
			// Remove port from IP if present
			if colonIndex := strings.LastIndex(clientIP, ":"); colonIndex != -1 {
				clientIP = clientIP[:colonIndex]
			}
			
			allowed := false
			for _, allowedIP := range allowedIPs {
				if clientIP == allowedIP || allowedIP == "127.0.0.1" && (clientIP == "::1" || clientIP == "localhost") {
					allowed = true
					break
				}
			}
			
			if !allowed {
				http.Error(w, "Forbidden - IP not allowed", http.StatusForbidden)
				
				log.Warn().
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("client_ip", clientIP).
					Msg("IP not in whitelist")
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

func isValidAPIKey(provided, expected string) bool {
	if provided == "" || expected == "" {
		return false
	}
	
	// Use constant-time comparison to prevent timing attacks
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// GordonClaims represents the JWT claims for Gordon tokens
type GordonClaims struct {
	Username string   `json:"username"`
	Roles    []string `json:"roles"`
	jwt.RegisteredClaims
}

// JWTAuth middleware provides JWT token authentication
func JWTAuth(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract token from Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "Authorization header required", http.StatusUnauthorized)
				return
			}

			// Check for Bearer token
			tokenString := ""
			if strings.HasPrefix(authHeader, "Bearer ") {
				tokenString = strings.TrimPrefix(authHeader, "Bearer ")
			} else {
				http.Error(w, "Bearer token required", http.StatusUnauthorized)
				return
			}

			// Parse and validate token
			token, err := jwt.ParseWithClaims(tokenString, &GordonClaims{}, func(token *jwt.Token) (interface{}, error) {
				// Validate signing method
				if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
				}
				return []byte(jwtSecret), nil
			})

			if err != nil {
				log.Warn().Err(err).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("remote_addr", r.RemoteAddr).
					Msg("Invalid JWT token")
				http.Error(w, "Invalid token", http.StatusUnauthorized)
				return
			}

			if !token.Valid {
				http.Error(w, "Invalid token", http.StatusUnauthorized)
				return
			}

			// Token is valid, continue
			next.ServeHTTP(w, r)
		})
	}
}

// GenerateJWTSecret generates a secure random secret for JWT signing
func GenerateJWTSecret() (string, error) {
	bytes := make([]byte, 32) // 256 bits
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate JWT secret: %w", err)
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

// GenerateJWTToken generates a JWT token for a user
func GenerateJWTToken(username string, roles []string, jwtSecret string, validFor time.Duration) (string, error) {
	claims := GordonClaims{
		Username: username,
		Roles:    roles,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "gordon",
			Subject:   username,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(validFor)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString([]byte(jwtSecret))
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT token: %w", err)
	}

	return tokenString, nil
}

// GenerateSecureAPIKey generates a secure random API key
func GenerateSecureAPIKey() (string, error) {
	bytes := make([]byte, 24) // 192 bits, base64 encoded will be ~32 chars
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate API key: %w", err)
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}