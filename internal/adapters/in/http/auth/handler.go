// Package auth implements the HTTP adapter for authentication endpoints.
package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

// InternalAuth holds credentials for internal loopback registry access.
type InternalAuth struct {
	Username string
	Password string
}

// Handler handles authentication requests at /auth/*.
type Handler struct {
	authSvc      in.AuthService
	internalAuth InternalAuth
	log          zerowrap.Logger
}

// NewHandler creates a new auth handler.
func NewHandler(authSvc in.AuthService, internalAuth InternalAuth, log zerowrap.Logger) *Handler {
	return &Handler{
		authSvc:      authSvc,
		internalAuth: internalAuth,
		log:          log,
	}
}

// PasswordRequest represents the request body for POST /auth/password.
type PasswordRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// PasswordResponse represents the response from POST /auth/password.
type PasswordResponse struct {
	Token     string `json:"token"`
	ExpiresIn int    `json:"expires_in"`
	IssuedAt  string `json:"issued_at"`
}

// ServeHTTP routes requests to the appropriate handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/auth"), "/")

	switch path {
	case "/password":
		h.handlePassword(w, r)
	case "/token":
		h.handleToken(w, r)
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "not found"})
	}
}

// handlePassword handles POST /auth/password requests.
// Validates username/password and returns a long-lived JWT (7 days).
// Only works when auth type is "password" - returns error for "token" auth type.
func (h *Handler) handlePassword(w http.ResponseWriter, r *http.Request) {
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "http",
		zerowrap.FieldHandler: "auth",
		zerowrap.FieldMethod:  r.Method,
		zerowrap.FieldPath:    r.URL.Path,
	})
	log := zerowrap.FromCtx(ctx)

	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "method not allowed"})
		return
	}

	// Check if auth is enabled
	if h.authSvc == nil || !h.authSvc.IsEnabled() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "authentication is disabled"})
		return
	}

	// Password endpoint only works with password auth type
	if h.authSvc.GetAuthType() != domain.AuthTypePassword {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{
			Error: "password authentication not configured, use token-based auth",
		})
		return
	}

	// Parse request body
	var req PasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "invalid request body"})
		return
	}

	if req.Username == "" || req.Password == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "username and password are required"})
		return
	}

	// Validate password
	if !h.authSvc.ValidatePassword(ctx, req.Username, req.Password) {
		log.Debug().
			Str("username", req.Username).
			Msg("password authentication failed")
		w.Header().Set("WWW-Authenticate", `Basic realm="Gordon"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "invalid credentials"})
		return
	}

	// Generate 7-day token with full access (push, pull, admin)
	expiry := 7 * 24 * time.Hour
	scopes := []string{"push", "pull", "admin:*:*"}

	token, err := h.authSvc.GenerateToken(ctx, req.Username, scopes, expiry)
	if err != nil {
		log.Error().Err(err).Msg("failed to generate token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "failed to generate token"})
		return
	}

	response := PasswordResponse{
		Token:     token,
		ExpiresIn: int(expiry.Seconds()),
		IssuedAt:  time.Now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error().Err(err).Msg("failed to encode password response")
	}

	log.Debug().
		Str("username", req.Username).
		Int("expires_in", response.ExpiresIn).
		Msg("long-lived token issued via password auth")
}

// handleToken handles GET /auth/token requests.
// This is the Docker Registry v2 token server endpoint.
// Issues short-lived access tokens for registry access.
func (h *Handler) handleToken(w http.ResponseWriter, r *http.Request) {
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "http",
		zerowrap.FieldHandler: "auth",
		zerowrap.FieldMethod:  r.Method,
		zerowrap.FieldPath:    r.URL.Path,
	})
	log := zerowrap.FromCtx(ctx)

	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "method not allowed"})
		return
	}

	// Check if auth is enabled
	if h.authSvc == nil || !h.authSvc.IsEnabled() {
		// If auth is disabled, return an anonymous token
		h.sendAnonymousToken(w, log)
		return
	}

	// Get credentials from Basic Auth
	username, password, ok := r.BasicAuth()
	if !ok {
		// No credentials provided - return anonymous token with limited scope
		h.sendAnonymousToken(w, log)
		return
	}

	// Check for internal registry auth (localhost requests with internal creds)
	var authenticated bool
	if isLocalhostRequest(r) && h.isInternalAuth(username, password) {
		authenticated = true
		log.Debug().Str("username", username).Msg("internal registry auth accepted")
	} else {
		// Validate credentials based on auth type
		switch h.authSvc.GetAuthType() {
		case domain.AuthTypePassword:
			authenticated = h.authSvc.ValidatePassword(ctx, username, password)
		case domain.AuthTypeToken:
			// For token auth, the password might be an existing JWT
			claims, err := h.authSvc.ValidateToken(ctx, password)
			if err == nil && claims.Subject == username {
				authenticated = true
			}
		}
	}

	if !authenticated {
		log.Debug().
			Str("username", username).
			Msg("token request authentication failed")
		w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "unauthorized"})
		return
	}

	// Parse requested scopes from query parameter
	requestedScopes := h.parseRequestedScopes(r, log)

	// Generate a short-lived access token (5 minutes) - not stored
	accessToken, err := h.authSvc.GenerateAccessToken(ctx, username, requestedScopes, 5*time.Minute)
	if err != nil {
		log.Error().Err(err).Msg("failed to generate access token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "internal server error"})
		return
	}

	response := dto.TokenResponse{
		Token:     accessToken,
		ExpiresIn: 300, // 5 minutes in seconds
		IssuedAt:  time.Now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error().Err(err).Msg("failed to encode token response")
	}

	log.Debug().
		Str("username", username).
		Int("expires_in", response.ExpiresIn).
		Msg("access token issued")
}

// sendAnonymousToken sends a token for anonymous/unauthenticated access.
func (h *Handler) sendAnonymousToken(w http.ResponseWriter, log zerowrap.Logger) {
	// For anonymous access, we issue a very short-lived token with limited scope
	response := dto.TokenResponse{
		Token:     "", // Empty token indicates limited access
		ExpiresIn: 60, // 1 minute
		IssuedAt:  time.Now().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Error().Err(err).Msg("failed to encode anonymous token response")
	}

	log.Debug().Msg("anonymous token issued")
}

// isLocalhostRequest checks if the request originates from localhost.
// SECURITY: Uses RemoteAddr (server-set) instead of Host header (client-spoofable).
func isLocalhostRequest(r *http.Request) bool {
	host := r.RemoteAddr
	// RemoteAddr includes port, e.g., "127.0.0.1:12345" or "[::1]:12345"
	return strings.HasPrefix(host, "127.") ||
		strings.HasPrefix(host, "[::1]") ||
		strings.HasPrefix(host, "::1")
}

// parseRequestedScopes extracts and validates scope parameters from the request.
// Per Docker Registry v2 auth spec, scope format is: repository:name:actions
// Example: GET /auth/token?scope=repository:myrepo:push,pull&scope=repository:other:pull
func (h *Handler) parseRequestedScopes(r *http.Request, log zerowrap.Logger) []string {
	scopeParams := r.URL.Query()["scope"]

	if len(scopeParams) == 0 {
		// Default scope: pull from any repository for backward compatibility
		log.Debug().Msg("no scope requested, using default pull scope")
		return []string{"repository:*:pull"}
	}

	var validScopes []string
	for _, scopeStr := range scopeParams {
		// Validate the scope format
		scope, err := domain.ParseScope(scopeStr)
		if err != nil {
			log.Debug().
				Err(err).
				Str("scope", scopeStr).
				Msg("invalid scope format, skipping")
			continue
		}

		// Only allow repository scopes
		if scope.Type != "repository" {
			log.Debug().
				Str("scope_type", scope.Type).
				Str("scope", scopeStr).
				Msg("unsupported scope type, skipping")
			continue
		}

		validScopes = append(validScopes, scopeStr)
	}

	if len(validScopes) == 0 {
		// All requested scopes were invalid, return default
		log.Debug().Msg("all requested scopes invalid, using default pull scope")
		return []string{"repository:*:pull"}
	}

	log.Debug().
		Strs("scopes", validScopes).
		Msg("parsed requested scopes")
	return validScopes
}

// isInternalAuth checks if the credentials match internal registry auth.
func (h *Handler) isInternalAuth(username, password string) bool {
	if h.internalAuth.Username == "" || h.internalAuth.Password == "" {
		return false
	}
	return username == h.internalAuth.Username && password == h.internalAuth.Password
}
