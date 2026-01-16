package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "gordon/internal/boundaries/in/mocks"
	"gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func ctxWithScopes(scopes ...string) context.Context {
	ctx := context.Background()
	return context.WithValue(ctx, ContextKeyScopes, scopes)
}

// Routes endpoint tests

func TestHandler_RoutesGet_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "routes read access granted",
			scopes:     []string{"admin:routes:read"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "routes wildcard access granted",
			scopes:     []string{"admin:routes:*"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "all admin access granted",
			scopes:     []string{"admin:*:*"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "only write access denied",
			scopes:     []string{"admin:routes:write"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong resource denied",
			scopes:     []string{"admin:secrets:read"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no scopes denied",
			scopes:     []string{},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{}).Maybe()
			}

			req := httptest.NewRequest("GET", "/admin/routes", nil)
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestHandler_RoutesPost_RequiresWriteScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	routeJSON := `{"domain": "app.example.com", "image": "myapp:latest"}`

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "routes write access granted",
			scopes:     []string{"admin:routes:write"},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "routes wildcard access granted",
			scopes:     []string{"admin:routes:*"},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "all admin access granted",
			scopes:     []string{"admin:*:*"},
			wantStatus: http.StatusCreated,
		},
		{
			name:       "only read access denied",
			scopes:     []string{"admin:routes:read"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong resource denied",
			scopes:     []string{"admin:secrets:write"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusCreated {
				configSvc.EXPECT().AddRoute(mock.Anything, mock.AnythingOfType("domain.Route")).Return(nil).Maybe()
			}

			req := httptest.NewRequest("POST", "/admin/routes", bytes.NewBufferString(routeJSON))
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestHandler_RoutesPut_RequiresWriteScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	routeJSON := `{"image": "myapp:v2"}`

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "routes write access granted",
			scopes:     []string{"admin:routes:write"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "only read access denied",
			scopes:     []string{"admin:routes:read"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				configSvc.EXPECT().UpdateRoute(mock.Anything, mock.AnythingOfType("domain.Route")).Return(nil).Maybe()
			}

			req := httptest.NewRequest("PUT", "/admin/routes/app.example.com", bytes.NewBufferString(routeJSON))
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestHandler_RoutesDelete_RequiresWriteScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "routes write access granted",
			scopes:     []string{"admin:routes:write"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "only read access denied",
			scopes:     []string{"admin:routes:read"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				configSvc.EXPECT().RemoveRoute(mock.Anything, "app.example.com").Return(nil).Maybe()
			}

			req := httptest.NewRequest("DELETE", "/admin/routes/app.example.com", nil)
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// Secrets endpoint tests

func TestHandler_SecretsGet_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "secrets read access granted",
			scopes:     []string{"admin:secrets:read"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "secrets wildcard access granted",
			scopes:     []string{"admin:secrets:*"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "all admin access granted",
			scopes:     []string{"admin:*:*"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "only write access denied",
			scopes:     []string{"admin:secrets:write"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong resource denied",
			scopes:     []string{"admin:routes:read"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				secretSvc.EXPECT().ListKeys(mock.Anything, "app.example.com").Return([]string{}, nil).Maybe()
			}

			req := httptest.NewRequest("GET", "/admin/secrets/app.example.com", nil)
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestHandler_SecretsPost_RequiresWriteScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	secretsJSON := `{"API_KEY": "secret123"}`

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "secrets write access granted",
			scopes:     []string{"admin:secrets:write"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "only read access denied",
			scopes:     []string{"admin:secrets:read"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong resource denied",
			scopes:     []string{"admin:routes:write"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				secretSvc.EXPECT().Set(mock.Anything, "app.example.com", mock.AnythingOfType("map[string]string")).Return(nil).Maybe()
			}

			req := httptest.NewRequest("POST", "/admin/secrets/app.example.com", bytes.NewBufferString(secretsJSON))
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

func TestHandler_SecretsDelete_RequiresWriteScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "secrets write access granted",
			scopes:     []string{"admin:secrets:write"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "only read access denied",
			scopes:     []string{"admin:secrets:read"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				secretSvc.EXPECT().Delete(mock.Anything, "app.example.com", "API_KEY").Return(nil).Maybe()
			}

			req := httptest.NewRequest("DELETE", "/admin/secrets/app.example.com/API_KEY", nil)
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// Status endpoint tests

func TestHandler_Status_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "status read access granted",
			scopes:     []string{"admin:status:read"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "all admin access granted",
			scopes:     []string{"admin:*:*"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "wrong resource denied",
			scopes:     []string{"admin:routes:read"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{}).Maybe()
				configSvc.EXPECT().GetRegistryDomain().Return("registry.example.com").Maybe()
				configSvc.EXPECT().GetRegistryPort().Return(5000).Maybe()
				configSvc.EXPECT().GetServerPort().Return(8080).Maybe()
				configSvc.EXPECT().IsAutoRouteEnabled().Return(true).Maybe()
				configSvc.EXPECT().IsNetworkIsolationEnabled().Return(false).Maybe()
			}

			req := httptest.NewRequest("GET", "/admin/status", nil)
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// Config endpoint tests

func TestHandler_Config_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "config read access granted",
			scopes:     []string{"admin:config:read"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "all admin access granted",
			scopes:     []string{"admin:*:*"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "wrong resource denied",
			scopes:     []string{"admin:routes:read"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				configSvc.EXPECT().GetServerPort().Return(8080).Maybe()
				configSvc.EXPECT().GetRegistryPort().Return(5000).Maybe()
				configSvc.EXPECT().GetRegistryDomain().Return("registry.example.com").Maybe()
				configSvc.EXPECT().GetDataDir().Return("/var/lib/gordon").Maybe()
				configSvc.EXPECT().IsAutoRouteEnabled().Return(true).Maybe()
				configSvc.EXPECT().IsNetworkIsolationEnabled().Return(false).Maybe()
				configSvc.EXPECT().GetNetworkPrefix().Return("gordon").Maybe()
				configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{}).Maybe()
				configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{}).Maybe()
			}

			req := httptest.NewRequest("GET", "/admin/config", nil)
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// Reload endpoint tests

func TestHandler_Reload_RequiresWriteScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{
			name:       "config write access granted",
			scopes:     []string{"admin:config:write"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "all admin access granted",
			scopes:     []string{"admin:*:*"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "only read access denied",
			scopes:     []string{"admin:config:read"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "wrong resource denied",
			scopes:     []string{"admin:routes:write"},
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				configSvc.EXPECT().Load(mock.Anything).Return(nil).Maybe()
			}

			req := httptest.NewRequest("POST", "/admin/reload", nil)
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}

// Functional tests

func TestHandler_RoutesGet_ReturnsRoutes(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	expectedRoutes := []domain.Route{
		{Domain: "app1.example.com", Image: "app1:latest"},
		{Domain: "app2.example.com", Image: "app2:v1.0"},
	}
	configSvc.EXPECT().GetRoutes(mock.Anything).Return(expectedRoutes)

	req := httptest.NewRequest("GET", "/admin/routes", nil)
	req = req.WithContext(ctxWithScopes("admin:routes:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response map[string]any
	err := json.NewDecoder(rec.Body).Decode(&response)
	assert.NoError(t, err)

	routes, ok := response["routes"].([]any)
	assert.True(t, ok)
	assert.Len(t, routes, 2)
}

func TestHandler_RoutesGet_SingleRoute(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	configSvc.EXPECT().GetRoute(mock.Anything, "app.example.com").Return(&domain.Route{
		Domain: "app.example.com", Image: "app:latest",
	}, nil)

	req := httptest.NewRequest("GET", "/admin/routes/app.example.com", nil)
	req = req.WithContext(ctxWithScopes("admin:routes:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var route domain.Route
	err := json.NewDecoder(rec.Body).Decode(&route)
	assert.NoError(t, err)
	assert.Equal(t, "app.example.com", route.Domain)
	assert.Equal(t, "app:latest", route.Image)
}

func TestHandler_RoutesGet_NotFound(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	configSvc.EXPECT().GetRoute(mock.Anything, "nonexistent.example.com").Return(nil, domain.ErrRouteNotFound)

	req := httptest.NewRequest("GET", "/admin/routes/nonexistent.example.com", nil)
	req = req.WithContext(ctxWithScopes("admin:routes:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_RoutesPost_MissingFields(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		mockErr error
	}{
		{
			name:    "missing domain",
			body:    `{"image": "app:latest"}`,
			mockErr: domain.ErrRouteDomainEmpty,
		},
		{
			name:    "missing image",
			body:    `{"domain": "app.example.com"}`,
			mockErr: domain.ErrRouteImageEmpty,
		},
		{
			name:    "empty object",
			body:    `{}`,
			mockErr: domain.ErrRouteDomainEmpty,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configSvc := inmocks.NewMockConfigService(t)
			authSvc := inmocks.NewMockAuthService(t)
			containerSvc := inmocks.NewMockContainerService(t)
			secretSvc := inmocks.NewMockSecretService(t)

			handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

			configSvc.EXPECT().AddRoute(mock.Anything, mock.Anything).Return(tt.mockErr)

			req := httptest.NewRequest("POST", "/admin/routes", bytes.NewBufferString(tt.body))
			req = req.WithContext(ctxWithScopes("admin:routes:write"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestHandler_RoutesPut_MissingDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	req := httptest.NewRequest("PUT", "/admin/routes/", bytes.NewBufferString(`{"image": "app:latest"}`))
	req = req.WithContext(ctxWithScopes("admin:routes:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_RoutesDelete_MissingDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	req := httptest.NewRequest("DELETE", "/admin/routes/", nil)
	req = req.WithContext(ctxWithScopes("admin:routes:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_Secrets_MissingDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	req := httptest.NewRequest("GET", "/admin/secrets/", nil)
	req = req.WithContext(ctxWithScopes("admin:secrets:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_SecretsDelete_MissingKey(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	req := httptest.NewRequest("DELETE", "/admin/secrets/app.example.com", nil)
	req = req.WithContext(ctxWithScopes("admin:secrets:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	tests := []struct {
		method string
		path   string
	}{
		{"DELETE", "/admin/status"},
		{"PUT", "/admin/status"},
		{"GET", "/admin/reload"},
		{"DELETE", "/admin/reload"},
		{"POST", "/admin/config"},
		{"DELETE", "/admin/config"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req = req.WithContext(ctxWithScopes("admin:*:*"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		})
	}
}

func TestHandler_NotFound(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, testLogger())

	req := httptest.NewRequest("GET", "/admin/unknown", nil)
	req = req.WithContext(ctxWithScopes("admin:*:*"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
