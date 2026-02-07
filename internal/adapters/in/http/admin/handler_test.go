package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func ctxWithScopes(scopes ...string) context.Context {
	ctx := context.Background()
	return context.WithValue(ctx, domain.ContextKeyScopes, scopes)
}

// Routes endpoint tests

func TestHandler_RoutesGet_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, registrySvc, nil, testLogger(), nil)

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
				registrySvc.EXPECT().GetManifest(mock.Anything, "myapp", "latest").Return(nil, nil).Maybe()
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
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, registrySvc, nil, testLogger(), nil)

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
				registrySvc.EXPECT().GetManifest(mock.Anything, "myapp", "v2").Return(nil, nil).Maybe()
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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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
				secretSvc.EXPECT().ListKeysWithAttachments(mock.Anything, "app.example.com").Return([]string{}, nil, nil).Maybe()
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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

// Attachment secrets endpoint tests

func TestHandler_AttachmentSecretsPost(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	secretSvc.EXPECT().SetAttachment(mock.Anything, "app.example.com", "redis", mock.AnythingOfType("map[string]string")).Return(nil)

	body := `{"REDIS_URL": "redis://localhost:6379"}`
	req := httptest.NewRequest("POST", "/admin/secrets/app.example.com/attachments/redis", bytes.NewBufferString(body))
	req = req.WithContext(ctxWithScopes("admin:secrets:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		Status string `json:"status"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "updated", response.Status)
}

func TestHandler_AttachmentSecretsDelete(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	secretSvc.EXPECT().DeleteAttachment(mock.Anything, "app.example.com", "redis", "REDIS_URL").Return(nil)

	req := httptest.NewRequest("DELETE", "/admin/secrets/app.example.com/attachments/redis/REDIS_URL", nil)
	req = req.WithContext(ctxWithScopes("admin:secrets:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		Status string `json:"status"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "deleted", response.Status)
}

// Status endpoint tests

func TestHandler_Status_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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
				configSvc.EXPECT().Reload(mock.Anything).Return(nil).Maybe()
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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	var response struct {
		Routes []struct {
			Domain string `json:"domain"`
			Image  string `json:"image"`
			HTTPS  bool   `json:"https"`
		} `json:"routes"`
	}
	err := json.NewDecoder(rec.Body).Decode(&response)
	assert.NoError(t, err)

	assert.Len(t, response.Routes, 2)
}

func TestHandler_RoutesGet_SingleRoute(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	configSvc.EXPECT().GetRoute(mock.Anything, "app.example.com").Return(&domain.Route{
		Domain: "app.example.com", Image: "app:latest",
	}, nil)

	req := httptest.NewRequest("GET", "/admin/routes/app.example.com", nil)
	req = req.WithContext(ctxWithScopes("admin:routes:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var route struct {
		Domain string `json:"domain"`
		Image  string `json:"image"`
		HTTPS  bool   `json:"https"`
	}
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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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
			registrySvc := inmocks.NewMockRegistryService(t)

			handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, registrySvc, nil, testLogger(), nil)

			configSvc.EXPECT().AddRoute(mock.Anything, mock.Anything).Return(tt.mockErr)
			registrySvc.EXPECT().GetManifest(mock.Anything, mock.Anything, mock.Anything).Return(nil, nil).Maybe()

			req := httptest.NewRequest("POST", "/admin/routes", bytes.NewBufferString(tt.body))
			req = req.WithContext(ctxWithScopes("admin:routes:write"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
		})
	}
}

func TestHandler_RoutesPost_ImageNotFound(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, registrySvc, nil, testLogger(), nil)

	tests := []struct {
		name         string
		image        string
		body         string
		manifestName string
		manifestRef  string
	}{
		{
			name:         "tagged image not found",
			image:        "myapp:latest",
			body:         `{"domain": "app.example.com", "image": "myapp:latest"}`,
			manifestName: "myapp",
			manifestRef:  "latest",
		},
		{
			name:         "digest image not found",
			image:        "myapp@sha256:abc123",
			body:         `{"domain": "app.example.com", "image": "myapp@sha256:abc123"}`,
			manifestName: "myapp",
			manifestRef:  "sha256:abc123",
		},
		{
			name:         "implicit latest tag not found",
			image:        "myapp",
			body:         `{"domain": "app.example.com", "image": "myapp"}`,
			manifestName: "myapp",
			manifestRef:  "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registrySvc.EXPECT().GetManifest(mock.Anything, tt.manifestName, tt.manifestRef).Return(nil, domain.ErrManifestNotFound)

			req := httptest.NewRequest("POST", "/admin/routes", bytes.NewBufferString(tt.body))
			req = req.WithContext(ctxWithScopes("admin:routes:write"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), fmt.Sprintf("image '%s' not found", tt.image))
		})
	}
}

func TestHandler_RoutesPut_MissingDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

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

func TestHandler_Restart_InvalidDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	tests := []struct {
		name   string
		path   string
		status int
	}{
		{"path traversal", "/admin/restart/../../etc/passwd", http.StatusBadRequest},
		{"empty domain", "/admin/restart/", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.path, nil)
			req = req.WithContext(ctxWithScopes("admin:config:write"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.status, rec.Code)
		})
	}
}

func TestHandler_Restart_ContainerNotFound(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	containerSvc.EXPECT().Restart(mock.Anything, "nonexistent.example.com", false).Return(domain.ErrContainerNotFound)

	req := httptest.NewRequest("POST", "/admin/restart/nonexistent.example.com", nil)
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_Restart_GenericError(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	containerSvc.EXPECT().Restart(mock.Anything, "test.example.com", false).Return(fmt.Errorf("docker daemon error: secret details"))

	req := httptest.NewRequest("POST", "/admin/restart/test.example.com", nil)
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	// Verify internal error details are NOT leaked
	assert.NotContains(t, rec.Body.String(), "docker daemon error")
	assert.NotContains(t, rec.Body.String(), "secret details")
}

func TestHandler_Restart_Success(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	tests := []struct {
		name            string
		path            string
		withAttachments bool
	}{
		{
			name:            "without attachments",
			path:            "/admin/restart/test.example.com",
			withAttachments: false,
		},
		{
			name:            "with attachments",
			path:            "/admin/restart/test.example.com?attachments=true",
			withAttachments: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containerSvc.EXPECT().Restart(mock.Anything, "test.example.com", tt.withAttachments).Return(nil)

			req := httptest.NewRequest("POST", tt.path, nil)
			req = req.WithContext(ctxWithScopes("admin:config:write"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			var response struct {
				Status string `json:"status"`
				Domain string `json:"domain"`
			}
			assert.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
			assert.Equal(t, "restarted", response.Status)
			assert.Equal(t, "test.example.com", response.Domain)
		})
	}
}

func TestHandler_Tags_InvalidRepository(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	tests := []struct {
		name   string
		path   string
		status int
	}{
		{"path traversal", "/admin/tags/../../etc/passwd", http.StatusBadRequest},
		{"empty repo", "/admin/tags/", http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			req = req.WithContext(ctxWithScopes("admin:status:read"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.status, rec.Code)
		})
	}
}

func TestHandler_Tags_Success(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, registrySvc, nil, testLogger(), nil)

	registrySvc.EXPECT().ListTags(mock.Anything, "myapp").Return([]string{"latest", "v1.0"}, nil)

	req := httptest.NewRequest("GET", "/admin/tags/myapp", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var response struct {
		Repository string   `json:"repository"`
		Tags       []string `json:"tags"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "myapp", response.Repository)
	assert.Equal(t, []string{"latest", "v1.0"}, response.Tags)
}

func TestHandler_Tags_Error(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, registrySvc, nil, testLogger(), nil)

	registrySvc.EXPECT().ListTags(mock.Anything, "myapp").Return(nil, fmt.Errorf("registry error"))

	req := httptest.NewRequest("GET", "/admin/tags/myapp", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "registry error")
}

func TestHandler_AttachmentSecretsPost_RequiresWriteScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	body := `{"REDIS_URL": "redis://localhost:6379"}`
	req := httptest.NewRequest("POST", "/admin/secrets/app.example.com/attachments/redis", bytes.NewBufferString(body))
	req = req.WithContext(ctxWithScopes("admin:secrets:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandler_AttachmentSecretsDelete_RequiresWriteScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	req := httptest.NewRequest("DELETE", "/admin/secrets/app.example.com/attachments/redis/REDIS_URL", nil)
	req = req.WithContext(ctxWithScopes("admin:secrets:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHandler_AttachmentSecretsDelete_RequiresKey(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	req := httptest.NewRequest("DELETE", "/admin/secrets/app.example.com/attachments/redis", nil)
	req = req.WithContext(ctxWithScopes("admin:secrets:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid attachment path")
}

func TestHandler_AttachmentSecretsPost_InvalidJSON(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	body := `{not valid json`
	req := httptest.NewRequest("POST", "/admin/secrets/app.example.com/attachments/redis", bytes.NewBufferString(body))
	req = req.WithContext(ctxWithScopes("admin:secrets:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid JSON")
}

func TestHandler_NotFound(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	req := httptest.NewRequest("GET", "/admin/unknown", nil)
	req = req.WithContext(ctxWithScopes("admin:*:*"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_BackupsStatus(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	backupSvc := inmocks.NewMockBackupService(t)

	backupSvc.EXPECT().Status(mock.Anything).Return([]domain.BackupJob{{Domain: "app.example.com", DBName: "postgres", Status: domain.BackupStatusCompleted, FilePath: "/var/lib/gordon/backups/private.bak"}}, nil)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), backupSvc)

	req := httptest.NewRequest("GET", "/admin/backups/status", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "app.example.com")
	assert.NotContains(t, rec.Body.String(), "file_path")
}

func TestHandler_BackupsListDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	backupSvc := inmocks.NewMockBackupService(t)

	backupSvc.EXPECT().ListBackups(mock.Anything, "app.example.com").Return([]domain.BackupJob{{Domain: "app.example.com", DBName: "postgres", Status: domain.BackupStatusCompleted}}, nil)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), backupSvc)

	req := httptest.NewRequest("GET", "/admin/backups/app.example.com", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "app.example.com")
}

func TestHandler_BackupsRunDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	backupSvc := inmocks.NewMockBackupService(t)

	backupSvc.EXPECT().RunBackup(mock.Anything, "app.example.com", "postgres").Return(&domain.BackupResult{Job: domain.BackupJob{Domain: "app.example.com", DBName: "postgres", Status: domain.BackupStatusCompleted}}, nil)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), backupSvc)

	body := bytes.NewBufferString(`{"db":"postgres"}`)
	req := httptest.NewRequest("POST", "/admin/backups/app.example.com", body)
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "completed")
}

func TestHandler_BackupsRunDomain_ChunkedBodyIsDecoded(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	backupSvc := inmocks.NewMockBackupService(t)

	backupSvc.EXPECT().RunBackup(mock.Anything, "app.example.com", "postgres").Return(&domain.BackupResult{Job: domain.BackupJob{Domain: "app.example.com", DBName: "postgres", Status: domain.BackupStatusCompleted}}, nil)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), backupSvc)

	body := bytes.NewBufferString(`{"db":"postgres"}`)
	req := httptest.NewRequest("POST", "/admin/backups/app.example.com", body)
	req.ContentLength = -1
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "completed")
}

func TestHandler_BackupsDetectDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	backupSvc := inmocks.NewMockBackupService(t)

	backupSvc.EXPECT().DetectDatabases(mock.Anything, "app.example.com").Return([]domain.DBInfo{{Type: domain.DBTypePostgreSQL, Name: "postgres", Port: 5432}}, nil)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), backupSvc)

	req := httptest.NewRequest("GET", "/admin/backups/app.example.com/detect", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "postgres")
}
