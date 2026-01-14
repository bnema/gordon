package registry

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/in"
	"gordon/internal/domain"
)

// TokenResponse represents the response from the token server.
type TokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token,omitempty"`
	ExpiresIn   int    `json:"expires_in,omitempty"`
	IssuedAt    string `json:"issued_at,omitempty"`
}

// InternalAuth holds credentials for internal loopback registry access.
type InternalAuth struct {
	Username string
	Password string
}

// TokenHandler handles token server requests for Docker Registry authentication.
type TokenHandler struct {
	authSvc      in.AuthService
	internalAuth InternalAuth
	log          zerowrap.Logger
}

// NewTokenHandler creates a new token handler.
func NewTokenHandler(authSvc in.AuthService, internalAuth InternalAuth, log zerowrap.Logger) *TokenHandler {
	return &TokenHandler{
		authSvc:      authSvc,
		internalAuth: internalAuth,
		log:          log,
	}
}

// ServeHTTP implements http.Handler for the token endpoint.
func (h *TokenHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handleToken(w, r)
}

// handleToken handles GET /v2/token requests.
// This endpoint issues short-lived access tokens per the Docker Registry auth spec.
func (h *TokenHandler) handleToken(w http.ResponseWriter, r *http.Request) {
	ctx := zerowrap.CtxWithFields(r.Context(), map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "http",
		zerowrap.FieldHandler: "token",
		zerowrap.FieldMethod:  r.Method,
		zerowrap.FieldPath:    r.URL.Path,
	})
	log := zerowrap.FromCtx(ctx)

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse requested scopes from query parameter
	// Format: scope=repository:name:action1,action2 (can appear multiple times)
	requestedScopes := h.parseRequestedScopes(r, log)

	// Generate a short-lived access token (5 minutes)
	accessToken, err := h.authSvc.GenerateToken(ctx, username, requestedScopes, 5*time.Minute)
	if err != nil {
		log.Error().Err(err).Msg("failed to generate access token")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	response := TokenResponse{
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
func (h *TokenHandler) sendAnonymousToken(w http.ResponseWriter, log zerowrap.Logger) {
	// For anonymous access, we issue a very short-lived token with limited scope
	response := TokenResponse{
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
// Example: GET /v2/token?scope=repository:myrepo:push,pull&scope=repository:other:pull
func (h *TokenHandler) parseRequestedScopes(r *http.Request, log zerowrap.Logger) []string {
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
func (h *TokenHandler) isInternalAuth(username, password string) bool {
	if h.internalAuth.Username == "" || h.internalAuth.Password == "" {
		return false
	}
	return username == h.internalAuth.Username && password == h.internalAuth.Password
}
