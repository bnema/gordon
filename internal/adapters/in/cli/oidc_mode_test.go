package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

// These tests run real httptest servers (sockets) for the CI host and the
// runner's ID token endpoint, so they stay outside synctest; the promote
// watch loop is shortened through operationPollInterval instead.

const (
	oidcJWT          = "eyJhbGciOiJSUzI1NiJ9.ci-identity.sig"
	oidcRequestToken = "runner-request-secret"
	oidcCIToken      = "ci-access-secret-token"
)

type ciHostFake struct {
	mu        sync.Mutex
	paths     []string
	scopes    [][]string
	audiences []string
}

func (f *ciHostFake) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
}

func (f *ciHostFake) Paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

// oidcEnv starts a fake runner and a fake CI host and points the CLI at
// them in OIDC mode.
func oidcEnv(t *testing.T) (*ciHostFake, *httptest.Server) {
	t.Helper()
	configurePushRemoteInferenceTestEnv(t, "")
	fake := &ciHostFake{}
	runner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "bearer "+oidcRequestToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fake.mu.Lock()
		fake.audiences = append(fake.audiences, r.URL.Query().Get("audience"))
		fake.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"value": oidcJWT})
	}))
	t.Cleanup(runner.Close)
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.record(r)
		if r.URL.Path == "/auth/token" {
			if user, pass, ok := r.BasicAuth(); !ok || user != "oidc" || pass != oidcJWT {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fake.mu.Lock()
			fake.scopes = append(fake.scopes, r.URL.Query()["scope"])
			fake.mu.Unlock()
			_ = json.NewEncoder(w).Encode(dto.TokenResponse{Token: oidcCIToken, ExpiresIn: 900})
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+oidcCIToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/-/ci/apps/e2e-demo/promote":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(dto.AppDeployResponse{Op: r.Header.Get("Idempotency-Key"), App: "e2e-demo", Status: dto.AppStatusRunning})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/-/ci/apps/e2e-demo/operations/by-key/"):
			_ = json.NewEncoder(w).Encode(dto.AppDeployResponse{Op: "k", App: "e2e-demo", Status: "success", Outcome: "success"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(host.Close)
	origNewClient := newOIDCClient
	t.Cleanup(func() { newOIDCClient = origNewClient })
	newOIDCClient = func(baseURL string, opts ...remote.ClientOption) *remote.Client {
		return remote.NewClient(baseURL, append(opts, remote.WithHTTPClient(host.Client()))...)
	}
	t.Setenv("GORDON_OIDC_TOKEN", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", runner.URL+"?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", oidcRequestToken)
	t.Setenv("GORDON_OIDC_AUDIENCE", "https://ci.example.com")
	t.Setenv("GORDON_TOKEN", "stored-gordon-token-must-not-be-used")
	remoteFlag = host.URL
	origAudience := oidcAudienceFlag
	t.Cleanup(func() { oidcAudienceFlag = origAudience })
	oidcAudienceFlag = ""
	return fake, host
}

func assertNoSecrets(t *testing.T, outputs ...string) {
	t.Helper()
	for _, out := range outputs {
		for _, secret := range []string{oidcJWT, oidcRequestToken, oidcCIToken} {
			assert.NotContains(t, out, secret)
		}
	}
}

func TestAppPromote_OIDCModeUsesCIHostRoutes(t *testing.T) {
	fake, _ := oidcEnv(t)
	orig := operationPollInterval
	operationPollInterval = time.Millisecond
	t.Cleanup(func() { operationPollInterval = orig })

	cmd := newAppPromoteCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"e2e-demo", "--service", "web", "--image", "e2e-demo@sha256:" + strings.Repeat("a", 64), "--json"})
	require.NoError(t, cmd.Execute(), stderr.String())

	var doc dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &doc))
	assert.Equal(t, "success", doc.Outcome)
	paths := fake.Paths()
	require.GreaterOrEqual(t, len(paths), 3)
	assert.Equal(t, "GET /auth/token", paths[0])
	assert.Equal(t, "POST /-/ci/apps/e2e-demo/promote", paths[1])
	assert.True(t, strings.HasPrefix(paths[2], "GET /-/ci/apps/e2e-demo/operations/by-key/"))
	for _, p := range paths {
		assert.NotContains(t, p, "/admin", "OIDC mode never calls the admin API")
	}
	assert.Equal(t, []string{"https://ci.example.com"}, fake.audiences)
	assertNoSecrets(t, stdout.String(), stderr.String())
}

func TestAppPromote_OIDCAudienceFlagAndMissingAudience(t *testing.T) {
	fake, host := oidcEnv(t)
	orig := operationPollInterval
	operationPollInterval = time.Millisecond
	t.Cleanup(func() { operationPollInterval = orig })
	t.Setenv("GORDON_OIDC_AUDIENCE", "")

	run := func(args ...string) (string, error) {
		root := NewRootCmd()
		var stdout, stderr bytes.Buffer
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetArgs(append([]string{"apps", "promote", "e2e-demo", "--service", "web", "--image", "e2e-demo@sha256:" + strings.Repeat("a", 64), "--remote", host.URL, "--json"}, args...))
		err := root.Execute()
		assertNoSecrets(t, stdout.String(), stderr.String())
		if err != nil {
			assertNoSecrets(t, err.Error())
		}
		return stdout.String(), err
	}
	_, err := run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--oidc-audience")
	assert.Empty(t, fake.Paths(), "nothing is sent without an audience")

	_, err = run("--oidc-audience", "https://flag.example.com")
	require.NoError(t, err)
	assert.Equal(t, []string{"https://flag.example.com"}, fake.audiences)
}

func TestAppPromote_GordonOIDCTokenTakesPrecedence(t *testing.T) {
	fake, _ := oidcEnv(t)
	orig := operationPollInterval
	operationPollInterval = time.Millisecond
	t.Cleanup(func() { operationPollInterval = orig })
	t.Setenv("GORDON_OIDC_TOKEN", oidcJWT)
	t.Setenv("GORDON_OIDC_AUDIENCE", "")

	cmd := newAppPromoteCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"e2e-demo", "--service", "web", "--image", "e2e-demo@sha256:" + strings.Repeat("a", 64)})
	require.NoError(t, cmd.Execute())
	assert.Empty(t, fake.audiences, "the runner endpoint is never called")
}

func TestImagesPush_OIDCModePushesToCIHostWithScopedToken(t *testing.T) {
	fake, host := oidcEnv(t)
	old := newImageOpsFn
	t.Cleanup(func() { newImageOpsFn = old })
	ops := &recordingPushOps{exists: true}
	newImageOpsFn = func() (pushImageOps, error) {
		resolved, client, err := resolveOIDCRemote()
		require.NoError(t, err)
		require.NotNil(t, client)
		auth, err := ciRegistryAuth(context.Background(), client, "e2e-demo")
		require.NoError(t, err)
		assert.Equal(t, "Bearer "+oidcCIToken, auth)
		assert.Equal(t, host.URL, resolved.URL)
		return ops, nil
	}

	cmd := newPushCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"e2e-demo:v1", "--tag", "v1", "--json"})
	require.NoError(t, cmd.Execute(), stderr.String())

	ciHost := strings.TrimPrefix(host.URL, "https://")
	var result map[string]string
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &result))
	assert.Equal(t, ciHost+"/e2e-demo", result["repository"], "the CI host is the push registry")
	assert.Equal(t, "v1", result["tag"])
	assert.Contains(t, ops.pushes, ciHost+"/e2e-demo:v1")
	for _, p := range fake.Paths() {
		assert.NotContains(t, p, "/admin", "OIDC push never calls the admin API")
	}
	require.NotEmpty(t, fake.scopes)
	assert.Equal(t, []string{"repository:e2e-demo:push,pull"}, fake.scopes[len(fake.scopes)-1])
	assertNoSecrets(t, stdout.String(), stderr.String())
}

func TestAppPromote_OIDCModeRefusesPlaintextRemote(t *testing.T) {
	fake, _ := oidcEnv(t)
	var hits int
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(plain.Close)
	t.Setenv("GORDON_OIDC_TOKEN", oidcJWT)

	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"apps", "promote", "e2e-demo", "--service", "web", "--image", "e2e-demo@sha256:" + strings.Repeat("a", 64), "--remote", plain.URL, "--json"})
	err := root.Execute()
	require.ErrorIs(t, err, remote.ErrCIInsecureTransport)
	assert.Contains(t, err.Error(), "https://")
	assertNoSecrets(t, stdout.String(), stderr.String(), err.Error())
	assert.Zero(t, hits, "nothing reached the plaintext remote")
	assert.Empty(t, fake.Paths())
	assert.Empty(t, fake.audiences, "the runner token endpoint is not even asked")
}
