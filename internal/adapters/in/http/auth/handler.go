// Package auth implements the HTTP adapter for authentication endpoints.
package auth

import (
	"context"
	"crypto/subtle"
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

const passwordSessionTTL = 24 * time.Hour

func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
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
// Validates username/password and returns a daily session JWT.
// Only works when auth type is "password" - returns error for "token" auth type.
func (h *Handler) handlePassword(w http.ResponseWriter, r *http.Request) {
	setNoStoreHeaders(w)

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
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "authentication is required"})
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

	// Generate daily session token with full access (push, pull, admin).
	// This enforces regular re-authentication similar to short-session UX.
	expiry := passwordSessionTTL
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
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
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
	setNoStoreHeaders(w)

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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "authentication is required"})
		return
	}

	// Get credentials from Basic Auth
	username, password, ok := r.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "unauthorized"})
		return
	}

	authenticated, parentClaims := h.authenticateTokenCredentials(ctx, r, username, password, log)
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
	if parentClaims != nil {
		requestedScopes = h.intersectRequestedScopes(requestedScopes, parentClaims.Scopes, log)
		if len(requestedScopes) == 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(dto.ErrorResponse{Error: "forbidden: insufficient scope"})
			return
		}
	}

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
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
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

func (h *Handler) authenticateTokenCredentials(ctx context.Context, r *http.Request, username, password string, log zerowrap.Logger) (bool, *domain.TokenClaims) {
	if isLocalhostRequest(r) && h.isInternalAuth(username, password) {
		log.Debug().Str("username", username).Msg("internal registry auth accepted")
		return true, nil
	}

	switch h.authSvc.GetAuthType() {
	case domain.AuthTypePassword:
		return h.authSvc.ValidatePassword(ctx, username, password), nil
	case domain.AuthTypeToken:
		claims, err := h.authSvc.ValidateToken(ctx, password)
		if err == nil && claims.Subject == username {
			return true, claims
		}
	}

	return false, nil
}

func (h *Handler) intersectRequestedScopes(requestedScopes, grantedScopes []string, log zerowrap.Logger) []string {
	var effective []string

	for _, reqScopeStr := range requestedScopes {
		reqScope, err := domain.ParseScope(reqScopeStr)
		if err != nil || reqScope.Type != domain.ScopeTypeRepository {
			continue
		}

		allowedActions := make([]string, 0, len(reqScope.Actions))
		for _, action := range reqScope.Actions {
			if hasGrantedRegistryAccess(grantedScopes, reqScope.Name, action) {
				allowedActions = append(allowedActions, action)
			}
		}

		if len(allowedActions) == 0 {
			continue
		}

		effective = append(effective, (&domain.Scope{
			Type:    reqScope.Type,
			Name:    reqScope.Name,
			Actions: allowedActions,
		}).String())
	}

	log.Debug().
		Strs("requested_scopes", requestedScopes).
		Strs("granted_scopes", grantedScopes).
		Strs("effective_scopes", effective).
		Msg("calculated effective scopes from parent token")

	return effective
}

func hasGrantedRegistryAccess(grantedScopes []string, repoName, action string) bool {
	for _, grantedScopeStr := range grantedScopes {
		scopeStr := strings.TrimSpace(grantedScopeStr)
		switch scopeStr {
		case domain.ScopeActionAll:
			return true
		case domain.ScopeActionPull, domain.ScopeActionPush:
			if scopeStr == action {
				return true
			}
		}

		scope, err := domain.ParseScope(scopeStr)
		if err != nil {
			continue
		}
		if scope.Type != domain.ScopeTypeRepository {
			continue
		}
		if scope.CanAccess(repoName, action) {
			return true
		}
	}

	return false
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
	usernameMatch := subtle.ConstantTimeCompare([]byte(username), []byte(h.internalAuth.Username)) == 1
	passwordMatch := subtle.ConstantTimeCompare([]byte(password), []byte(h.internalAuth.Password)) == 1
	return usernameMatch && passwordMatch
}
