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
