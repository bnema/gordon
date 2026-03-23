package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// newTestServer creates a mock server that handles admin token exchange
// and custom handlers for specific paths.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Handle admin token exchange for all tests — the client does this automatically.
		if r.URL.Path == "/auth/token" && r.URL.Query().Get("scope") == "admin:*:*" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(dto.TokenResponse{
				Token:     "ephemeral-admin-token",
				ExpiresIn: 900,
			}) //nolint:errcheck
			return
		}
		handler(w, r)
	}))
}

// TestExchangeRegistryAuth_Success verifies the happy path:
// VerifyAuth is called, then ExchangeRegistryToken is called with the
// subject from VerifyAuth, and the result is "Bearer <short-lived-token>".
func TestExchangeRegistryAuth_Success(t *testing.T) {
	const subject = "ci-bot"
	const shortToken = "short-lived-token-abc123"

	verifyHandled := false
	tokenHandled := false

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/auth/verify":
			verifyHandled = true
			w.Header().Set("Content-Type", "application/json")
			resp := dto.AuthVerifyResponse{
				Valid:   true,
				Subject: subject,
				Scopes:  []string{"push", "pull"},
			}
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
		case "/auth/token":
			// Registry token exchange (scope != admin:*:*)
			tokenHandled = true
			u, p, ok := r.BasicAuth()
			assert.True(t, ok, "expected basic auth")
			assert.Equal(t, subject, u, "expected subject as username")
			assert.NotEmpty(t, p, "expected token as password")

			w.Header().Set("Content-Type", "application/json")
			resp := dto.TokenResponse{Token: shortToken}
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
		default:
			http.NotFound(w, r)
		}
	})
	defer srv.Close()

	client := remote.NewClient(srv.URL, remote.WithToken("long-lived-token"))
	ops := &dockerImageOps{
		remoteClient: client,
	}

	got, err := ops.exchangeRegistryAuth(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "Bearer "+shortToken, got)
	assert.True(t, verifyHandled, "expected /admin/auth/verify to be called")
	assert.True(t, tokenHandled, "expected /auth/token to be called")
}

// TestExchangeRegistryAuth_VerifyAuthFails verifies that when VerifyAuth fails,
// exchangeRegistryAuth returns an error.
func TestExchangeRegistryAuth_VerifyAuthFails(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/verify" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	})
	defer srv.Close()

	client := remote.NewClient(srv.URL, remote.WithToken("bad-token"))
	ops := &dockerImageOps{
		remoteClient: client,
	}

	_, err := ops.exchangeRegistryAuth(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to verify token")
}

// TestExchangeRegistryAuth_TokenNotValid verifies that when VerifyAuth returns
// Valid=false, exchangeRegistryAuth returns an error.
func TestExchangeRegistryAuth_TokenNotValid(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/verify" {
			w.Header().Set("Content-Type", "application/json")
			resp := dto.AuthVerifyResponse{Valid: false}
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
			return
		}
		http.NotFound(w, r)
	})
	defer srv.Close()

	client := remote.NewClient(srv.URL, remote.WithToken("expired-token"))
	ops := &dockerImageOps{
		remoteClient: client,
	}

	_, err := ops.exchangeRegistryAuth(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "token is not valid")
}
