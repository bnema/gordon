// Package acme provides an HTTP handler for the ACME HTTP-01 challenge.
// It serves key authorizations at /.well-known/acme-challenge/{token}.
package acme

import (
	"context"
	"net/http"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// Prefix is the path prefix for ACME HTTP-01 challenge requests.
const Prefix = "/.well-known/acme-challenge/"

// ChallengeService defines the interface for retrieving HTTP-01 challenge tokens.
type ChallengeService interface {
	GetHTTP01Challenge(ctx context.Context, token string) (keyAuth string, ok bool)
}

// Handler serves ACME HTTP-01 challenge responses.
type Handler struct {
	svc ChallengeService
}

// NewHandler creates a new Handler with the given ChallengeService.
func NewHandler(svc ChallengeService) *Handler {
	return &Handler{svc: svc}
}

// ServeHTTP handles incoming HTTP requests for ACME HTTP-01 challenges.
// Only GET and HEAD methods are allowed; all others return 405 Method Not Allowed.
// The token must be a safe path component and must exist in the challenge store.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !strings.HasPrefix(r.URL.Path, Prefix) {
		http.NotFound(w, r)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, Prefix)
	if !domain.IsValidHTTP01Token(token) {
		http.NotFound(w, r)
		return
	}

	if h.svc == nil {
		http.NotFound(w, r)
		return
	}

	keyAuth, ok := h.svc.GetHTTP01Challenge(r.Context(), token)
	if !ok {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if r.Method == http.MethodHead {
		return
	}

	_, _ = w.Write([]byte(keyAuth))
}
