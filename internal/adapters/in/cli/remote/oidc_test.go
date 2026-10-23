package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
)

// These tests use real httptest servers (sockets), so they run outside
// synctest and observe requests synchronously.

const (
	testJWT          = "eyJhbGciOiJSUzI1NiJ9.oidc-identity.signature"
	testRequestToken = "runner-request-token"
	testCIToken      = "ci-access-token-value"
)

func clearOIDCEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{envOIDCToken, envOIDCAudience, envActionsRequestURL, envActionsRequestToken} {
		t.Setenv(key, "")
	}
}

// fakeIDTokenServer implements the ACTIONS_ID_TOKEN_REQUEST_* protocol.
func fakeIDTokenServer(t *testing.T, gotAudience *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "bearer "+testRequestToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"bad request token ` + testRequestToken + `"}`))
			return
		}
		assert.Equal(t, "2.0", r.URL.Query().Get("api-version"), "existing query is kept")
		*gotAudience = r.URL.Query().Get("audience")
		_ = json.NewEncoder(w).Encode(map[string]string{"value": testJWT})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveOIDCSource_Precedence(t *testing.T) {
	var aud string
	runner := fakeIDTokenServer(t, &aud)

	t.Run("no OIDC environment", func(t *testing.T) {
		clearOIDCEnv(t)
		src, err := ResolveOIDCSource("")
		require.NoError(t, err)
		assert.Nil(t, src)
	})

	t.Run("GORDON_OIDC_TOKEN wins over the runner", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv(envOIDCToken, "static-jwt")
		t.Setenv(envActionsRequestURL, runner.URL+"?api-version=2.0")
		t.Setenv(envActionsRequestToken, testRequestToken)
		src, err := ResolveOIDCSource("")
		require.NoError(t, err)
		require.NotNil(t, src)
		jwt, err := src.IDToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "static-jwt", jwt)
	})

	t.Run("runner without audience is an error", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv(envActionsRequestURL, runner.URL+"?api-version=2.0")
		t.Setenv(envActionsRequestToken, testRequestToken)
		_, err := ResolveOIDCSource("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--oidc-audience")
		assert.Contains(t, err.Error(), envOIDCAudience)
		assert.NotContains(t, err.Error(), testRequestToken)
	})

	t.Run("runner with env audience fetches the ID token", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv(envActionsRequestURL, runner.URL+"?api-version=2.0")
		t.Setenv(envActionsRequestToken, testRequestToken)
		t.Setenv(envOIDCAudience, "https://ci.example.com")
		src, err := ResolveOIDCSource("")
		require.NoError(t, err)
		jwt, err := src.IDToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, testJWT, jwt)
		assert.Equal(t, "https://ci.example.com", aud)
	})

	t.Run("flag audience overrides the env", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv(envActionsRequestURL, runner.URL+"?api-version=2.0")
		t.Setenv(envActionsRequestToken, testRequestToken)
		t.Setenv(envOIDCAudience, "https://env.example.com")
		src, err := ResolveOIDCSource("https://flag.example.com")
		require.NoError(t, err)
		_, err = src.IDToken(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "https://flag.example.com", aud)
	})

	t.Run("only one runner variable is not OIDC mode", func(t *testing.T) {
		clearOIDCEnv(t)
		t.Setenv(envActionsRequestURL, runner.URL)
		src, err := ResolveOIDCSource("")
		require.NoError(t, err)
		assert.Nil(t, src)
	})
}

func TestOIDCSource_FetchErrorNeverEchoesSecrets(t *testing.T) {
	var aud string
	runner := fakeIDTokenServer(t, &aud)
	src := &OIDCSource{requestURL: runner.URL + "?api-version=2.0", requestToken: "wrong-" + testRequestToken, audience: "aud"}
	_, err := src.IDToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.NotContains(t, err.Error(), testRequestToken)

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"value":""}`))
	}))
	defer empty.Close()
	_, err = (&OIDCSource{requestURL: empty.URL, requestToken: testRequestToken, audience: "aud"}).IDToken(context.Background())
	require.Error(t, err)
}

// fakeCIHost is a CI host: token exchange, promote and by-key lookup.
type fakeCIHost struct {
	t          *testing.T
	exchanges  atomic.Int32
	scopes     []string
	reject401  atomic.Int32
	promoteKey string
	promoted   dto.AppPromoteRequest
	paths      []string
}

func (f *fakeCIHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	switch {
	case r.URL.Path == "/auth/token":
		user, pass, ok := r.BasicAuth()
		if !ok || user != "oidc" || pass != testJWT {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.exchanges.Add(1)
		f.scopes = r.URL.Query()["scope"]
		assert.Equal(f.t, strings.TrimPrefix(r.Host, ""), r.URL.Query().Get("service"))
		_ = json.NewEncoder(w).Encode(dto.TokenResponse{Token: testCIToken, ExpiresIn: 900})
	case r.Header.Get("Authorization") != "Bearer "+testCIToken:
		w.WriteHeader(http.StatusUnauthorized)
	case f.reject401.Load() > 0:
		f.reject401.Add(-1)
		w.WriteHeader(http.StatusUnauthorized)
	case r.Method == http.MethodPost && r.URL.Path == "/-/ci/apps/web/promote":
		f.promoteKey = r.Header.Get("Idempotency-Key")
		require.NoError(f.t, json.NewDecoder(r.Body).Decode(&f.promoted))
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(dto.AppDeployResponse{Op: f.promoteKey, App: "web", Status: dto.AppStatusRunning})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/-/ci/apps/web/operations/by-key/"):
		_ = json.NewEncoder(w).Encode(dto.AppDeployResponse{Op: strings.TrimPrefix(r.URL.Path, "/-/ci/apps/web/operations/by-key/"), App: "web", Status: "success", Outcome: "success"})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func newOIDCClient(t *testing.T) (*Client, *fakeCIHost) {
	t.Helper()
	host := &fakeCIHost{t: t}
	srv := httptest.NewServer(host)
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, WithToken("ignored-stored-token"), WithOIDC(&OIDCSource{static: testJWT})), host
}

func TestClientOIDC_PromoteAndLookupUseCIHost(t *testing.T) {
	client, host := newOIDCClient(t)
	req := dto.AppPromoteRequest{Service: "web", Image: "ci.example.com/web@sha256:" + strings.Repeat("a", 64)}
	resp, key, err := client.PromoteApp(context.Background(), "web", req)
	require.NoError(t, err)
	assert.Equal(t, dto.AppStatusRunning, resp.Status)
	assert.Equal(t, key, host.promoteKey)
	assert.Equal(t, req, host.promoted)
	assert.Empty(t, host.scopes, "promotion needs no repository scope")

	op, err := client.OperationByKey(context.Background(), "web", key)
	require.NoError(t, err)
	assert.Equal(t, "success", op.Outcome)
	assert.Equal(t, int32(1), host.exchanges.Load(), "the ci-access token is cached")
	assert.Equal(t, []string{"GET /auth/token", "POST /-/ci/apps/web/promote", "GET /-/ci/apps/web/operations/by-key/" + key}, host.paths)
}

func TestClientOIDC_ReexchangesOnceAfter401(t *testing.T) {
	client, host := newOIDCClient(t)
	host.reject401.Store(1)
	_, err := client.OperationByKey(context.Background(), "web", "key-1")
	require.NoError(t, err)
	assert.Equal(t, int32(2), host.exchanges.Load())
}

func TestClientOIDC_RegistryTokenScopedToRepository(t *testing.T) {
	client, host := newOIDCClient(t)
	token, err := client.ExchangeCIToken(context.Background(), "repository:web:push,pull")
	require.NoError(t, err)
	assert.Equal(t, testCIToken, token)
	assert.Equal(t, []string{"repository:web:push,pull"}, host.scopes)

	client.ResetCIToken()
	_, err = client.ExchangeCIToken(context.Background(), "repository:web:push,pull")
	require.NoError(t, err)
	assert.Equal(t, int32(2), host.exchanges.Load(), "a reset forces a new exchange")
}

func TestClientOIDC_AdminCallsRefusedWithoutNetwork(t *testing.T) {
	client, host := newOIDCClient(t)
	_, err := client.VerifyAuth(context.Background())
	require.ErrorIs(t, err, ErrOIDCAdminUnavailable)
	_, err = client.GetStatus(context.Background())
	require.ErrorIs(t, err, ErrOIDCAdminUnavailable)
	_, _, err = client.DeployApp(context.Background(), "web", dto.AppDeployRequest{})
	require.ErrorIs(t, err, ErrOIDCAdminUnavailable)
	assert.Empty(t, host.paths, "no admin request left the client")

	host2, ok := client.CIRegistryHost()
	require.True(t, ok)
	assert.NotEmpty(t, host2)
	_, ok = NewClient("https://gordon.example.com").CIRegistryHost()
	assert.False(t, ok)
}

func TestClientOIDC_ExchangeFailureCarriesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(dto.AppError{Error: "identity-provider-unavailable", Message: "identity provider unavailable"})
	}))
	defer srv.Close()
	client := NewClient(srv.URL, WithOIDC(&OIDCSource{static: testJWT}))
	_, err := client.ExchangeCIToken(context.Background())
	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	assert.Equal(t, http.StatusServiceUnavailable, httpErr.StatusCode)
	assert.NotContains(t, err.Error(), testJWT)
}
