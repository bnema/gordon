package registry

import (
	"encoding/json"
	"net/http"
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

// TokenHandler handles token server requests for Docker Registry authentication.
type TokenHandler struct {
	authSvc in.AuthService
	log     zerowrap.Logger
}

// NewTokenHandler creates a new token handler.
func NewTokenHandler(authSvc in.AuthService, log zerowrap.Logger) *TokenHandler {
	return &TokenHandler{
		authSvc: authSvc,
		log:     log,
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

	// Validate credentials based on auth type
	var authenticated bool
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

	if !authenticated {
		log.Debug().
			Str("username", username).
			Msg("token request authentication failed")
		w.Header().Set("WWW-Authenticate", `Basic realm="Gordon Registry"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Generate a short-lived access token (5 minutes)
	accessToken, err := h.authSvc.GenerateToken(ctx, username, []string{"push", "pull"}, 5*time.Minute)
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
