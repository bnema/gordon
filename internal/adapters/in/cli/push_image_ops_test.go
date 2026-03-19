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

// TestExchangeRegistryAuth_Success verifies the happy path:
// VerifyAuth is called, then ExchangeRegistryToken is called with the
// subject from VerifyAuth, and the result is "Bearer <short-lived-token>".
func TestExchangeRegistryAuth_Success(t *testing.T) {
	const subject = "ci-bot"
	const shortToken = "short-lived-token-abc123"

	verifyHandled := false
	tokenHandled := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			tokenHandled = true
			// Verify basic auth was set with subject as username
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
	}))
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/verify" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admin/auth/verify" {
			w.Header().Set("Content-Type", "application/json")
			resp := dto.AuthVerifyResponse{Valid: false}
			json.NewEncoder(w).Encode(resp) //nolint:errcheck
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := remote.NewClient(srv.URL, remote.WithToken("expired-token"))
	ops := &dockerImageOps{
		remoteClient: client,
	}

	_, err := ops.exchangeRegistryAuth(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "token is not valid")
}
