package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	adminhttp "github.com/bnema/gordon/internal/adapters/in/http/admin"
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
	registryHandler, _ := createHTTPHandlers(svc, cfg, zerowrap.Default())

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

	registryHandler, _ := createHTTPHandlers(&services{}, cfg, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "/v2/test/manifests/latest", nil)
	req.RemoteAddr = "192.0.2.20:12345"

	rec := httptest.NewRecorder()
	registryHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, rec.Code)
	}
}

func TestCreateHTTPHandlers_AuthEnabled_ExposesAdminOnProxyForRegistryHost(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Auth.Enabled = true
	cfg.Server.RegistryDomain = "gordon.local"

	svc := &services{adminHandler: &adminhttp.Handler{}}
	_, proxyHandler := createHTTPHandlers(svc, cfg, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.Host = "gordon.local"
	req.RemoteAddr = "192.0.2.30:12345"

	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}
}

func TestCreateHTTPHandlers_AuthEnabled_BlocksAdminOnNonRegistryHost(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.Auth.Enabled = true
	cfg.Server.RegistryDomain = "gordon.local"

	svc := &services{adminHandler: &adminhttp.Handler{}}
	_, proxyHandler := createHTTPHandlers(svc, cfg, zerowrap.Default())

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.Host = "app.example.com"
	req.RemoteAddr = "192.0.2.30:12345"

	rec := httptest.NewRecorder()
	proxyHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rec.Code)
	}
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
