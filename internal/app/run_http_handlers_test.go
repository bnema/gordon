package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	adminhttp "github.com/bnema/gordon/internal/adapters/in/http/admin"
	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	out "github.com/bnema/gordon/internal/boundaries/out"
	proxyusecase "github.com/bnema/gordon/internal/usecase/proxy"
)

func newNotFoundProxyService(t *testing.T) *proxyusecase.Service {
	t.Helper()
	configSvc := inmocks.NewMockConfigService(t)
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
	configSvc.EXPECT().GetRoutes(mock.Anything).Return(nil).Maybe()
	containerSvc := inmocks.NewMockContainerService(t)
	containerSvc.EXPECT().Get(mock.Anything, mock.Anything).Return(nil, false).Maybe()
	return proxyusecase.NewService(nil, containerSvc, configSvc, proxyusecase.Config{})
}

// testAccessLogWriter is a thread-safe mock AccessLogWriter for tests.
type testAccessLogWriter struct {
	mu      sync.Mutex
	entries []out.AccessLogEntry
}

func (w *testAccessLogWriter) Write(entry out.AccessLogEntry) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.entries = append(w.entries, entry)
	return nil
}

func (w *testAccessLogWriter) snapshot() []out.AccessLogEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	result := make([]out.AccessLogEntry, len(w.entries))
	copy(result, w.entries)
	return result
}

func TestCreateAuthService_Disabled_ReturnsNilServices(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Auth.Enabled = false

	store, authSvc, err := createAuthService(context.Background(), cfg, zerowrap.Default())
	if err != nil {
		t.Fatalf("expected no error when auth is disabled, got %v", err)
	}
	if store != nil {
		t.Fatalf("expected nil token store when auth is disabled")
	}
	if authSvc != nil {
		t.Fatalf("expected nil auth service when auth is disabled")
	}
}

func TestCreateHTTPHandlers_LocalMode_DisablesAdminRoutes(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Auth.Enabled = false

	svc := &services{adminHandler: &adminhttp.Handler{}}
	registryHandler, _, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.0.2.10:12345"

	rec := httptest.NewRecorder()
	registryHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
}

func TestCreateHTTPHandlers_LocalMode_RestrictsRegistryToLoopback(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Auth.Enabled = false

	registryHandler, _, _ := createHTTPHandlers(&services{}, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v2/test/manifests/latest", nil)
	req.RemoteAddr = "192.0.2.20:12345"

	rec := httptest.NewRecorder()
	registryHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

// newTestCA creates a temporary CA adapter for integration tests.
func newTestCA(t *testing.T) *pkiadapter.CA {
	t.Helper()
	ca, err := pkiadapter.NewCA(t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	return ca
}

// newOnboardingConfig returns a Config with trusted proxy CIDRs and TLS enabled.
func newOnboardingConfig() Config {
	cfg := Config{}
	cfg.Server.Port = 8088
	cfg.Server.TLSPort = 8443
	cfg.Server.ProxyAllowedIPs = []string{"173.245.48.0/20"} // Cloudflare sample
	return cfg
}

// directAddr is an IP outside the trusted proxy CIDR range.
const directAddr = "203.0.113.10:12345"

// trustedProxyAddr is an IP inside the 173.245.48.0/20 range.
const trustedProxyAddr = "173.245.48.5:12345"

func TestCreateHTTPHandlers_DirectHTTPRoot_ServesOnboarding(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Body.String(), "Trust CA Certificate")
}

func TestCreateHTTPHandlers_DirectHTTPWellKnownGordonCACertPath_ServesCACert(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/gordon/ca.crt", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/x-x509-ca-cert")
	assert.Contains(t, rec.Body.String(), "BEGIN CERTIFICATE")
}

func TestCreateHTTPHandlers_DirectHTTPWellKnownGordonOnboarding_ServesOnboarding(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/gordon/", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Body.String(), "Trust CA Certificate")
}

func TestCreateHTTPHandlers_DirectHTTPCertPath_ServesCACert(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/ca.crt", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/x-x509-ca-cert")
	assert.Contains(t, rec.Body.String(), "BEGIN CERTIFICATE")
}

func TestCreateHTTPHandlers_DirectHTTPUnknownPath_ReturnsForbidden(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/web", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/plain")
}

func TestCreateHTTPHandlers_TrustedProxyHTTP_DoesNotServeOnboarding(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = trustedProxyAddr
	req.Host = "app.example.com"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	// Trusted proxy traffic should NOT get the onboarding page.
	// It should flow through to the normal proxy handler (which returns 502/404/etc,
	// but definitely not onboarding HTML).
	assert.NotContains(t, rec.Body.String(), "Trust CA Certificate")
}

func TestCreateHTTPHandlers_TrustedProxyCACertPath_DoesNotServeOnboardingResource(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/ca.crt", nil)
	req.RemoteAddr = trustedProxyAddr
	req.Host = "app.example.com"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	// Trusted proxy traffic must NOT leak the internal CA cert.
	assert.NotEqual(t, "application/x-x509-ca-cert", rec.Header().Get("Content-Type"))
	assert.NotContains(t, rec.Body.String(), "BEGIN CERTIFICATE")
}

func TestCreateHTTPHandlers_ForceHTTPSRedirect_DoesNotBypassDirectOnboarding(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	cfg.Server.ForceHTTPSRedirect = true
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	// Direct onboarding must win over force_https_redirect.
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Trust CA Certificate")
}

func TestCreateHTTPHandlers_HTTPSOnboarding_RemainsAvailableOnGordonDomain(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	cfg.Server.GordonDomain = "gordon.example.com"
	svc := &services{caAdapter: ca, proxySvc: newNotFoundProxyService(t)}

	_, _, httpsHandler := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/ca", nil)
	req.RemoteAddr = directAddr
	req.Host = "gordon.example.com"
	rec := httptest.NewRecorder()
	httpsHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Trust CA Certificate")
}

func TestCreateHTTPHandlers_HTTPSOnboarding_DoesNotPreemptAppHostCAPath(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	cfg.Server.GordonDomain = "gordon.example.com"
	svc := &services{caAdapter: ca, proxySvc: newNotFoundProxyService(t)}

	_, _, httpsHandler := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/ca", nil)
	req.RemoteAddr = directAddr
	req.Host = "app.example.com"
	rec := httptest.NewRecorder()
	httpsHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotContains(t, rec.Body.String(), "Trust CA Certificate")
}

func TestCreateHTTPHandlers_HTTPSOnboarding_HEADRoutesRemainAvailable(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	cfg.Server.GordonDomain = "gordon.example.com"
	svc := &services{caAdapter: ca, proxySvc: newNotFoundProxyService(t)}

	_, _, httpsHandler := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	// Use httptest.NewServer so Go's net/http server strips the body for HEAD.
	// httptest.NewRecorder does not apply server-level HEAD body stripping.
	ts := httptest.NewServer(httpsHandler)
	defer ts.Close()

	for _, path := range []string{"/ca", "/ca.crt", "/ca.mobileconfig"} {
		req, err := http.NewRequest(http.MethodHead, ts.URL+path, nil)
		require.NoError(t, err)
		req.Host = "gordon.example.com"
		resp, err := ts.Client().Do(req)
		require.NoError(t, err)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())

		assert.Equal(t, http.StatusOK, resp.StatusCode, "HEAD %s should return 200", path)
		assert.Empty(t, body, "HEAD %s should have empty body", path)
	}
}

func TestCreateHTTPHandlers_DirectHTTPOnboarding_HEADRoutesRemainAvailable(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = directAddr
		r.Host = "o2.bnema.dev"
		httpHandler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	for _, tc := range []struct {
		path        string
		contentType string
	}{
		{path: "/ca", contentType: "text/html"},
		{path: "/ca.crt", contentType: "application/x-x509-ca-cert"},
	} {
		resp, err := ts.Client().Head(ts.URL + tc.path)
		require.NoError(t, err)

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())

		assert.Equal(t, http.StatusOK, resp.StatusCode, "HEAD %s should return 200", tc.path)
		assert.Contains(t, resp.Header.Get("Content-Type"), tc.contentType)
		assert.Empty(t, body, "HEAD %s should have empty body", tc.path)
	}
}

func TestCreateHTTPHandlers_DirectHTTPForbiddenHEAD_ReturnsForbidden(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = directAddr
		r.Host = "o2.bnema.dev"
		httpHandler.ServeHTTP(w, r)
	}))
	defer ts.Close()

	resp, err := ts.Client().Head(ts.URL + "/web")
	require.NoError(t, err)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Empty(t, body, "HEAD 403 should have empty body")
}

func TestCreateHTTPHandlers_DirectHTTPACMEChallenge_Returns404(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/test-token", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestCreateHTTPHandlers_TLSDisabled_DoesNotServeHTTPOnboarding(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	cfg.Server.TLSPort = 0 // TLS disabled
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	// With TLS disabled, onboarding should NOT be served.
	assert.NotContains(t, rec.Body.String(), "Trust CA Certificate")
}

type recordingPublicTLSRuntime struct {
	reconcileCalls int
	loopCalls      int
	reconcileErr   error
}

func (r *recordingPublicTLSRuntime) Reconcile(context.Context) error {
	r.reconcileCalls++
	return r.reconcileErr
}

func (r *recordingPublicTLSRuntime) StartRenewalLoop(context.Context, time.Duration) <-chan struct{} {
	r.loopCalls++
	done := make(chan struct{})
	close(done)
	return done
}

func TestStartPublicTLSRuntime_ReconcilesAndStartsRenewalLoop(t *testing.T) {
	t.Parallel()

	runtime := &recordingPublicTLSRuntime{}
	err := startPublicTLSRuntime(context.Background(), runtime, zerowrap.Default())

	require.NoError(t, err)
	assert.Equal(t, 1, runtime.reconcileCalls)
	assert.Equal(t, 1, runtime.loopCalls)
}

func TestStartPublicTLSRuntime_DoesNotStartRenewalLoopWhenReconcileFails(t *testing.T) {
	t.Parallel()

	runtime := &recordingPublicTLSRuntime{reconcileErr: errors.New("boom")}
	err := startPublicTLSRuntime(context.Background(), runtime, zerowrap.Default())

	require.Error(t, err)
	assert.Equal(t, 1, runtime.reconcileCalls)
	assert.Equal(t, 0, runtime.loopCalls)
}

func TestCreateHTTPHandlers_TLSConfiguredWithoutCA_FailsStartup(t *testing.T) {
	t.Parallel()
	// Verify that initPKI returns an error when TLS is enabled but CA init fails.
	// We test this by providing a non-writable data dir.
	si := &serviceInit{
		ctx: context.Background(),
		cfg: Config{},
		log: zerowrap.Default(),
		svc: &services{},
	}
	si.cfg.Server.TLSPort = 8443
	dataDir := t.TempDir()
	blockingPath := filepath.Join(dataDir, "not-a-directory")
	require.NoError(t, os.WriteFile(blockingPath, []byte("blocking file"), 0600))
	// Point data dir to a file path so PKI directory creation fails portably.
	si.cfg.Server.DataDir = blockingPath

	err := si.initPKI()
	assert.Error(t, err, "initPKI should fail when CA cannot be initialized")
}

func TestBuildRegistryCIDRAllowlistMiddleware_InvalidEntries_DenyAll(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Server.RegistryAllowedIPs = []string{"not-a-cidr"}

	mw := buildRegistryCIDRAllowlistMiddleware(cfg, nil, zerowrap.Default())
	if mw == nil {
		t.Fatal("expected non-nil allowlist middleware")
	}

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v2/test/manifests/latest", nil)
	req.RemoteAddr = "127.0.0.1:12345"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

// TestAccessLog_AdminRejectedByLoopbackOnly_IsLogged verifies that requests
// to /admin/ blocked by loopbackOnly() still produce an access-log entry.
// This exercises the outer-deny path: AccessLogger wraps the top-level
// handler so even gates that run before any inner middleware are logged.
func TestAccessLog_AdminRejectedByLoopbackOnly_IsLogged(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Auth.Enabled = true // required for admin routes to be registered

	svc := &services{adminHandler: &adminhttp.Handler{}}
	aw := &testAccessLogWriter{}
	registryHandler, _, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), aw)

	// Non-loopback IP — loopbackOnly() will reject this with 403 before any
	// inner middleware runs.
	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "192.0.2.10:12345"

	rec := httptest.NewRecorder()
	registryHandler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	entries := aw.snapshot()
	require.Len(t, entries, 1, "access log must capture the loopback-rejected admin request")
	assert.Equal(t, http.StatusForbidden, entries[0].Status)
	assert.Equal(t, "/admin/status", entries[0].Path)
}

// TestAccessLog_RegistryRejectedByLoopbackOnly_IsLogged verifies that requests
// to /v2/ blocked by loopbackOnly() in local mode still produce an access-log entry.
func TestAccessLog_RegistryRejectedByLoopbackOnly_IsLogged(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Auth.Enabled = false // local mode: /v2/ is wrapped by loopbackOnly()

	svc := &services{}
	aw := &testAccessLogWriter{}
	registryHandler, _, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), aw)

	// Non-loopback IP — loopbackOnly() rejects with 403.
	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.RemoteAddr = "192.0.2.20:12345"

	rec := httptest.NewRecorder()
	registryHandler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	entries := aw.snapshot()
	require.Len(t, entries, 1, "access log must capture the loopback-rejected registry request")
	assert.Equal(t, http.StatusForbidden, entries[0].Status)
}

// TestAccessLog_DenyAllProxy_IsLogged verifies that requests rejected by the
// fail-closed denyAllHandler (triggered by an all-invalid proxy_allowed_ips
// config) still produce an access-log entry.
func TestAccessLog_DenyAllProxy_IsLogged(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	// All-invalid entries cause buildProxyCIDRAllowlistMiddleware to return
	// denyAllHandler, which wraps the proxy mux unconditionally with 403.
	cfg.Server.ProxyAllowedIPs = []string{"not-a-valid-ip"}

	svc := &services{}
	aw := &testAccessLogWriter{}
	_, httpProxyHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default(), aw)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.5:54321"

	rec := httptest.NewRecorder()
	httpProxyHandler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	entries := aw.snapshot()
	require.Len(t, entries, 1, "access log must capture the fail-closed proxy denial")
	assert.Equal(t, http.StatusForbidden, entries[0].Status)
}
