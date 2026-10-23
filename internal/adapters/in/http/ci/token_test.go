package ci

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/domain"
)

func tokenRequest(user, password, query string) *http.Request {
	req := request(http.MethodGet, "/auth/token"+query, nil)
	if user != "" || password != "" {
		req.SetBasicAuth(user, password)
	}
	return req
}

func decodeToken(t *testing.T, rec *httptest.ResponseRecorder) dto.TokenResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"token", "expires_in", "issued_at"}, keys, "same shape as the main token endpoint")
	var resp dto.TokenResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestToken_ExchangesOIDCForScopedCIAccess(t *testing.T) {
	h := newHarness(t)
	rec := h.serve(tokenRequest("oidc", "jwt-web", "?service=ci.example.com&scope=repository:web:push,pull&scope=repository:other:push&scope=admin:*:*&scope=repository:*:push"))
	resp := decodeToken(t, rec)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, int((15 * time.Minute).Seconds()), resp.ExpiresIn)

	principal, scopes, err := h.auth.ValidateCIAccessToken(context.Background(), resp.Token)
	require.NoError(t, err)
	assert.Equal(t, webPrincipal, principal)
	assert.Equal(t, []string{"repository:web:push,pull"}, scopes, "only policy repositories survive")

	_, err = h.auth.ValidateToken(context.Background(), resp.Token)
	assert.Error(t, err, "the minted token is not valid on the main host")
}

func TestToken_ScopeOutsidePolicyIsForbidden(t *testing.T) {
	h := newHarness(t)
	rec := h.serve(tokenRequest("oidc", "jwt-web", "?scope=repository:other:push,pull"))
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = h.serve(tokenRequest("oidc", "jwt-nopolicy", "?scope=repository:web:push"))
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec = h.serve(tokenRequest("oidc", "jwt-nopolicy", ""))
	assert.Equal(t, http.StatusForbidden, rec.Code, "a principal without any policy gets nothing")
}

func TestToken_LoginWithoutScopeMintsUnscopedToken(t *testing.T) {
	h := newHarness(t)
	resp := decodeToken(t, h.serve(tokenRequest("ci", "jwt-web", "?service=ci.example.com&account=ci")))
	principal, scopes, err := h.auth.ValidateCIAccessToken(context.Background(), resp.Token)
	require.NoError(t, err)
	assert.Equal(t, webPrincipal, principal)
	assert.Empty(t, scopes)
}

func TestToken_InvalidCredentials(t *testing.T) {
	h := newHarness(t)
	for name, req := range map[string]*http.Request{
		"no basic":    tokenRequest("", "", "?scope=repository:web:push"),
		"invalid jwt": tokenRequest("oidc", "forged", "?scope=repository:web:push"),
		"bearer":      withBearer(request(http.MethodGet, "/auth/token", nil), "jwt-web"),
	} {
		t.Run(name, func(t *testing.T) {
			rec := h.serve(req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, `Basic realm="Gordon CI"`, rec.Header().Get("WWW-Authenticate"))
			assert.Empty(t, rec.Header().Get("Retry-After"))
		})
	}
}

func TestToken_IdentityProviderUnavailableIs503(t *testing.T) {
	h := newHarness(t)
	rec := h.serve(tokenRequest("oidc", "unavailable", "?scope=repository:web:push"))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "30", rec.Header().Get("Retry-After"))
}

func TestToken_OversizedJWTRejectedBeforeVerify(t *testing.T) {
	h := newHarness(t)
	rec := h.serve(tokenRequest("oidc", strings.Repeat("a", maxJWTBytes+1), "?scope=repository:web:push"))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Zero(t, h.identity.calls.Load(), "an oversized JWT never reaches Verify")

	h.identity.tokens[strings.Repeat("b", maxJWTBytes)] = webPrincipal
	decodeToken(t, h.serve(tokenRequest("oidc", strings.Repeat("b", maxJWTBytes), "?scope=repository:web:push")))
}

func TestToken_NeverLogsSecretsAndRedactsUsername(t *testing.T) {
	h := newHarness(t)
	h.identity.tokens["eyJsecret.jwt.value"] = webPrincipal
	rec := h.serve(tokenRequest("very-secret-user", "eyJsecret.jwt.value", "?scope=repository:web:push"))
	resp := decodeToken(t, rec)
	h.serve(tokenRequest("very-secret-user", "eyJforged.jwt.value", "?scope=repository:web:push"))

	logs := h.logs.String()
	assert.NotContains(t, logs, "eyJsecret.jwt.value")
	assert.NotContains(t, logs, "eyJforged.jwt.value")
	assert.NotContains(t, logs, resp.Token)
	assert.NotContains(t, logs, "very-secret-user")
	assert.Contains(t, logs, "v***r", "the username is logged redacted")
}

func TestToken_RateLimited(t *testing.T) {
	var allowed atomic.Int32
	allowed.Store(2)
	limit := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if allowed.Add(-1) < 0 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	h := newHarness(t, func(d *Deps) { d.TokenRateLimit = limit })
	assert.Equal(t, http.StatusOK, h.serve(tokenRequest("oidc", "jwt-web", "?scope=repository:web:push")).Code)
	assert.Equal(t, http.StatusUnauthorized, h.serve(tokenRequest("oidc", "forged", "")).Code)
	assert.Equal(t, http.StatusTooManyRequests, h.serve(tokenRequest("oidc", "jwt-web", "?scope=repository:web:push")).Code)
	assert.Equal(t, int32(2), h.identity.calls.Load(), "a limited request never reaches Verify")

	token := h.ciToken(webPrincipal)
	assert.Equal(t, http.StatusAccepted, h.serve(withBearer(request(http.MethodGet, "/v2/", nil), token)).Code,
		"the limiter covers only the token endpoint")
}

// TestToken_BoundsWaitersOnIdentityProvider runs in a synctest bubble: the
// handlers are called directly with recorders (no sockets), and Verify
// blocks until its context ends, as a JWKS refresh wait does.
func TestToken_BoundsWaitersOnIdentityProvider(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, func(d *Deps) { d.MaxTokenWaiters = 2 })
		h.identity.block = func(ctx context.Context) error {
			<-ctx.Done()
			return errors.Join(domain.ErrIdentityProviderUnavailable, ctx.Err())
		}

		start := time.Now()
		results := make(chan *httptest.ResponseRecorder, 2)
		for range 2 {
			go func() { results <- h.serve(tokenRequest("oidc", "jwt-web", "")) }()
		}
		synctest.Wait()
		require.Equal(t, int32(2), h.identity.calls.Load(), "both waiters reached Verify")

		rec := h.serve(tokenRequest("oidc", "jwt-web", ""))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code, "a waiter beyond the cap is refused at once")
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))
		assert.Zero(t, time.Since(start))
		assert.Equal(t, int32(2), h.identity.calls.Load())

		for range 2 {
			rec := <-results
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Equal(t, "30", rec.Header().Get("Retry-After"))
		}
		assert.Equal(t, maxVerifyWait, time.Since(start), "each waiter gives up at the verify deadline")

		// The slots are released.
		h.identity.block = nil
		decodeToken(t, h.serve(tokenRequest("oidc", "jwt-web", "?scope=repository:web:push")))
	})
}
