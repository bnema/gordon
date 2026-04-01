package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adminhttp "github.com/bnema/gordon/internal/adapters/in/http/admin"
	pkiadapter "github.com/bnema/gordon/internal/adapters/out/pki"
	"github.com/bnema/zerowrap"
)

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
	registryHandler, _, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

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

	registryHandler, _, _ := createHTTPHandlers(&services{}, cfg, zerowrap.Default())

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

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
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

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

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

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

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

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

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

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

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

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	// Direct onboarding must win over force_https_redirect.
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Trust CA Certificate")
}

func TestCreateHTTPHandlers_HTTPSOnboarding_RemainsAvailable(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, _, httpsHandler := createHTTPHandlers(svc, cfg, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "/ca", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpsHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Trust CA Certificate")
}

func TestCreateHTTPHandlers_HTTPSOnboarding_HEADRoutesRemainAvailable(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, _, httpsHandler := createHTTPHandlers(svc, cfg, zerowrap.Default())

	// Use httptest.NewServer so Go's net/http server strips the body for HEAD.
	// httptest.NewRecorder does not apply server-level HEAD body stripping.
	ts := httptest.NewServer(httpsHandler)
	defer ts.Close()

	for _, path := range []string{"/ca", "/ca.crt", "/ca.mobileconfig"} {
		resp, err := ts.Client().Head(ts.URL + path)
		require.NoError(t, err)
		resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode, "HEAD %s should return 200", path)
	}
}

func TestCreateHTTPHandlers_DirectHTTPOnboarding_HEADRoutesRemainAvailable(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

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

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

	req := httptest.NewRequest(http.MethodHead, "/web", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, rec.Body.String(), "HEAD 403 should have empty body")
}

func TestCreateHTTPHandlers_DirectHTTPACMEChallenge_Returns404(t *testing.T) {
	t.Parallel()
	ca := newTestCA(t)
	cfg := newOnboardingConfig()
	svc := &services{caAdapter: ca}

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

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

	_, httpHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = directAddr
	req.Host = "o2.bnema.dev"
	rec := httptest.NewRecorder()
	httpHandler.ServeHTTP(rec, req)

	// With TLS disabled, onboarding should NOT be served.
	assert.NotContains(t, rec.Body.String(), "Trust CA Certificate")
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
	// Point data dir to a non-existent path under /proc to guarantee failure.
	si.cfg.Server.DataDir = "/proc/nonexistent/path"

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
