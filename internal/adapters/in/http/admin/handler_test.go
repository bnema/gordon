package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

type stubImageService struct {
	listImagesFunc func(context.Context) ([]domain.ImageInfo, error)
	pruneFunc      func(context.Context, domain.ImagePruneOptions) (domain.ImagePruneReport, error)
}

type noopReloadTrigger struct{}

func (noopReloadTrigger) Trigger(context.Context) error { return nil }

type reloadTriggerRecorder struct {
	calls        int
	err          error
	ctxErrAtCall error
	hasDeadline  bool
}

func (r *reloadTriggerRecorder) Trigger(ctx context.Context) error {
	r.calls++
	r.ctxErrAtCall = ctx.Err()
	_, r.hasDeadline = ctx.Deadline()
	return r.err
}

func (s *stubImageService) ListImages(ctx context.Context) ([]domain.ImageInfo, error) {
	if s.listImagesFunc == nil {
		return nil, nil
	}
	return s.listImagesFunc(ctx)
}

func (s *stubImageService) Prune(ctx context.Context, opts domain.ImagePruneOptions) (domain.ImagePruneReport, error) {
	if s.pruneFunc == nil {
		return domain.ImagePruneReport{}, nil
	}
	return s.pruneFunc(ctx, opts)
}

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func ctxWithScopes(scopes ...string) context.Context {
	ctx := context.Background()
	return context.WithValue(ctx, domain.ContextKeyScopes, scopes)
}

func newScopedTestServer(t *testing.T, handler http.Handler, scopes ...string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(ctxWithScopes(scopes...)))
	}))
	t.Cleanup(server.Close)

	return server
}

func newTestHandler(t *testing.T, opts ...func(*HandlerDeps)) *Handler {
	t.Helper()
	deps := HandlerDeps{
		ConfigSvc:     inmocks.NewMockConfigService(t),
		AuthSvc:       inmocks.NewMockAuthService(t),
		ContainerSvc:  inmocks.NewMockContainerService(t),
		HealthSvc:     inmocks.NewMockHealthService(t),
		SecretSvc:     inmocks.NewMockSecretService(t),
		Log:           testLogger(),
		ReloadTrigger: noopReloadTrigger{},
	}
	for _, opt := range opts {
		opt(&deps)
	}
	return NewHandler(deps)
}

func TestHandler_VolumesGet_RequiresVolumesReadScope(t *testing.T) {
	volumeSvc := inmocks.NewMockVolumeService(t)
	handler := newTestHandler(t, func(d *HandlerDeps) { d.VolumeSvc = volumeSvc })

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{name: "volumes read access granted", scopes: []string{"admin:volumes:read"}, wantStatus: http.StatusOK},
		{name: "all admin access granted", scopes: []string{"admin:*:*"}, wantStatus: http.StatusOK},
		{name: "status read denied", scopes: []string{"admin:status:read"}, wantStatus: http.StatusForbidden},
		{name: "volumes write denied", scopes: []string{"admin:volumes:write"}, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				volumeSvc.EXPECT().ListVolumes(mock.Anything).Return([]*domain.VolumeInfo{}, nil).Once()
			}
			server := newScopedTestServer(t, handler, tt.scopes...)
			resp, err := http.Get(server.URL + "/admin/volumes")
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
		})
	}
}

func TestHandler_VolumesPrune_RequiresVolumesWriteScope(t *testing.T) {
	volumeSvc := inmocks.NewMockVolumeService(t)
	handler := newTestHandler(t, func(d *HandlerDeps) { d.VolumeSvc = volumeSvc })

	tests := []struct {
		name       string
		scopes     []string
		wantStatus int
	}{
		{name: "volumes write access granted", scopes: []string{"admin:volumes:write"}, wantStatus: http.StatusOK},
		{name: "all admin access granted", scopes: []string{"admin:*:*"}, wantStatus: http.StatusOK},
		{name: "config write denied", scopes: []string{"admin:config:write"}, wantStatus: http.StatusForbidden},
		{name: "volumes read denied", scopes: []string{"admin:volumes:read"}, wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantStatus == http.StatusOK {
				volumeSvc.EXPECT().PruneVolumes(mock.Anything, true).Return(&domain.VolumePruneReport{}, []*domain.VolumeInfo{}, nil).Once()
			}
			server := newScopedTestServer(t, handler, tt.scopes...)
			resp, err := http.Post(server.URL+"/admin/volumes/prune", "application/json", bytes.NewBufferString(`{"dry_run":true}`))
			require.NoError(t, err)
			defer resp.Body.Close()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
		})
	}
}

// Routes endpoint tests

func TestHandler_SecretsGet_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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

func TestHandler_Status_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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
				configSvc.EXPECT().GetRegistryDomain().Return("registry.example.com").Maybe()
				configSvc.EXPECT().GetRegistryPort().Return(5000).Maybe()
				configSvc.EXPECT().GetServerPort().Return(8080).Maybe()
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

func TestHandler_Config_HidesSensitiveInventoryByDefault(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	handler := newTestHandler(t, func(d *HandlerDeps) { d.ConfigSvc = configSvc })

	configSvc.EXPECT().GetServerPort().Return(8080).Once()
	configSvc.EXPECT().GetRegistryPort().Return(5000).Once()
	configSvc.EXPECT().GetRegistryDomain().Return("registry.example.com").Once()
	configSvc.EXPECT().IsNetworkIsolationEnabled().Return(false).Once()
	configSvc.EXPECT().GetNetworkPrefix().Return("gordon").Once()
	configSvc.EXPECT().GetExternalRoutes().Return(map[string]string{"db.example.com": "http://10.0.0.5:5432"}).Once()

	server := newScopedTestServer(t, handler, "admin:config:read")
	resp, err := http.Get(server.URL + "/admin/config")
	require.NoError(t, err)
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := string(bodyBytes)
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &raw))
	serverConfig := raw["server"].(map[string]any)
	assert.NotContains(t, serverConfig, "data_dir")
	assert.NotContains(t, body, "/var/lib/gordon")
	assert.NotContains(t, body, "10.0.0.5")
}

func TestHandler_Config_RequiresReadScope(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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
				configSvc.EXPECT().IsNetworkIsolationEnabled().Return(false).Maybe()
				configSvc.EXPECT().GetNetworkPrefix().Return("gordon").Maybe()
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
	reloadTrigger := &reloadTriggerRecorder{}

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.ReloadTrigger = reloadTrigger
	})

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
			reloadTrigger.calls = 0

			req := httptest.NewRequest("POST", "/admin/reload", nil)
			req = req.WithContext(ctxWithScopes(tt.scopes...))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantStatus == http.StatusOK {
				assert.Equal(t, 1, reloadTrigger.calls)
			} else {
				assert.Zero(t, reloadTrigger.calls)
			}
		})
	}
}

func TestHandler_Reload_ReturnsServerErrorOnTriggerFailure(t *testing.T) {
	reloadTrigger := &reloadTriggerRecorder{err: errors.New("reload failed")}

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ReloadTrigger = reloadTrigger
	})

	req := httptest.NewRequest("POST", "/admin/reload", nil)
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, 1, reloadTrigger.calls)
}

func TestHandler_Reload_UsesDetachedContextWithDeadline(t *testing.T) {
	reloadTrigger := &reloadTriggerRecorder{}

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ReloadTrigger = reloadTrigger
	})

	reqCtx, cancel := context.WithCancel(ctxWithScopes("admin:config:write"))
	cancel()

	req := httptest.NewRequest("POST", "/admin/reload", nil)
	req = req.WithContext(reqCtx)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 1, reloadTrigger.calls)
	require.NoError(t, reloadTrigger.ctxErrAtCall)
	assert.True(t, reloadTrigger.hasDeadline)
}

// Functional tests

func TestHandler_Secrets_MissingDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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

func TestHandler_Tags_InvalidRepository(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.RegistrySvc = registrySvc
	})

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

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.RegistrySvc = registrySvc
	})

	registrySvc.EXPECT().ListTags(mock.Anything, "myapp").Return(nil, fmt.Errorf("registry error"))

	req := httptest.NewRequest("GET", "/admin/tags/myapp", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "registry error")
}

func TestHandler_Tags_URLDecodedRepository(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.RegistrySvc = registrySvc
	})

	registrySvc.EXPECT().ListTags(mock.Anything, "repo/app").Return([]string{"latest"}, nil)

	req := httptest.NewRequest("GET", "/admin/tags/repo%2Fapp", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"repository":"repo/app","tags":["latest"]}`, rec.Body.String())
}

func TestHandler_NotFound(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
	})

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

	backupSvc.EXPECT().Status(mock.Anything).Return([]domain.BackupJob{{App: "shop", Service: "api", DBName: "orders", Status: domain.BackupStatusCompleted, FilePath: "/var/lib/gordon/backups/private.bak"}}, nil)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.BackupSvc = backupSvc
	})

	req := httptest.NewRequest("GET", "/admin/backups/status", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "shop")
	assert.Contains(t, rec.Body.String(), "orders")
	assert.NotContains(t, rec.Body.String(), "file_path")
}

func TestHandler_BackupsListApp(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	backupSvc := inmocks.NewMockBackupService(t)

	backupSvc.EXPECT().ListBackups(mock.Anything, "shop").Return([]domain.BackupJob{{App: "shop", Service: "api", DBName: "orders", Status: domain.BackupStatusCompleted}}, nil)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.BackupSvc = backupSvc
	})

	req := httptest.NewRequest("GET", "/admin/backups/shop", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "shop")
}

func TestHandler_BackupsRunApp(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	backupSvc := inmocks.NewMockBackupService(t)

	backupSvc.EXPECT().RunBackup(mock.Anything, "shop", "api", "orders").Return(&domain.BackupResult{Job: domain.BackupJob{App: "shop", Service: "api", DBName: "orders", Status: domain.BackupStatusCompleted}}, nil)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.BackupSvc = backupSvc
	})

	body := bytes.NewBufferString(`{"service":"api","database":"orders"}`)
	req := httptest.NewRequest("POST", "/admin/backups/shop", body)
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "completed")
}

func TestHandler_VolumeBackupsRunDomain_ReturnsPartialResults(t *testing.T) {
	volumeBackupSvc := inmocks.NewMockVolumeBackupService(t)
	runErr := errors.New("one volume failed")
	jobs := []domain.VolumeBackupJob{{ID: "v1", App: "shop", Service: "api", VolumeName: "data", Status: domain.BackupStatusCompleted}}
	volumeBackupSvc.EXPECT().RunVolumeBackups(mock.Anything, "shop", "api", "data").Return(jobs, runErr)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.VolumeBackupSvc = volumeBackupSvc
	})

	body := bytes.NewBufferString(`{"service":"api","volume":"data"}`)
	req := httptest.NewRequest("POST", "/admin/backups/volumes/shop", body)
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusPartialContent, rec.Code)
	var result dto.VolumeBackupRunResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.Equal(t, "partial", result.Status)
	assert.Equal(t, runErr.Error(), result.Error)
	require.Len(t, result.Backups, 1)
	assert.Equal(t, "v1", result.Backups[0].ID)
}

func TestHandler_VolumeBackupsRunDomain_ReturnsServerErrorWithoutResults(t *testing.T) {
	volumeBackupSvc := inmocks.NewMockVolumeBackupService(t)
	volumeBackupSvc.EXPECT().RunVolumeBackups(mock.Anything, "shop", "", "").Return(nil, errors.New("runtime unavailable"))

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.VolumeBackupSvc = volumeBackupSvc
	})

	req := httptest.NewRequest("POST", "/admin/backups/volumes/shop", bytes.NewBufferString(`{}`))
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "failed to run volume backups")
}

func TestHandler_BackupsRunApp_ChunkedBodyIsDecoded(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	backupSvc := inmocks.NewMockBackupService(t)

	backupSvc.EXPECT().RunBackup(mock.Anything, "shop", "api", "orders").Return(&domain.BackupResult{Job: domain.BackupJob{App: "shop", Service: "api", DBName: "orders", Status: domain.BackupStatusCompleted}}, nil)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.BackupSvc = backupSvc
	})

	body := bytes.NewBufferString(`{"service":"api","database":"orders"}`)
	req := httptest.NewRequest("POST", "/admin/backups/shop", body)
	req.ContentLength = -1
	req = req.WithContext(ctxWithScopes("admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "completed")
}

func TestHandler_LogsRequireLogsReadAndRedact(t *testing.T) {
	logSvc := inmocks.NewMockLogService(t)
	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.LogSvc = logSvc
	})

	t.Run("status read denied", func(t *testing.T) {
		server := newScopedTestServer(t, handler, "admin:status:read")
		resp, err := http.Get(server.URL + "/admin/logs")
		require.NoError(t, err)
		defer resp.Body.Close()
		bodyBytes, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		assert.Equal(t, http.StatusForbidden, resp.StatusCode)
		assert.Contains(t, string(bodyBytes), "insufficient permissions for logs:read")
	})

	t.Run("logs read allowed and redacted", func(t *testing.T) {
		logSvc.EXPECT().GetProcessLogs(mock.Anything, 50).Return([]string{"PASSWORD=hunter2", `{"API_KEY":"abc123"}`}, nil).Once()

		server := newScopedTestServer(t, handler, "admin:logs:read")
		resp, err := http.Get(server.URL + "/admin/logs")
		require.NoError(t, err)
		defer resp.Body.Close()
		bodyBytes, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		body := string(bodyBytes)

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, body, "PASSWORD=[REDACTED]")
		assert.Contains(t, body, `\"API_KEY\":\"[REDACTED]\"`)
		assert.NotContains(t, body, "hunter2")
		assert.NotContains(t, body, "abc123")
	})
}

func TestHandler_ImagesGet_ReturnsMappedList(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	createdAt := time.Date(2026, time.February, 8, 14, 30, 0, 0, time.UTC)
	imageSvc := &stubImageService{
		listImagesFunc: func(context.Context) ([]domain.ImageInfo, error) {
			return []domain.ImageInfo{
				{
					Repository: "myapp",
					Tag:        "latest",
					Size:       1024,
					Created:    createdAt,
					ID:         "sha256:abc123",
					Dangling:   false,
				},
				{
					Repository: "",
					Tag:        "",
					Size:       512,
					Created:    createdAt,
					ID:         "sha256:def456",
					Dangling:   true,
				},
			}, nil
		},
	}

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.ImageSvc = imageSvc
	})

	req := httptest.NewRequest("GET", "/admin/images", nil)
	req = req.WithContext(ctxWithScopes("admin:status:read"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		Images []struct {
			Repository string    `json:"repository"`
			Tag        string    `json:"tag"`
			Size       int64     `json:"size"`
			Created    time.Time `json:"created"`
			ID         string    `json:"id"`
			Dangling   bool      `json:"dangling"`
		} `json:"images"`
	}

	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Len(t, response.Images, 2)
	assert.Equal(t, "myapp", response.Images[0].Repository)
	assert.Equal(t, "latest", response.Images[0].Tag)
	assert.Equal(t, int64(1024), response.Images[0].Size)
	assert.Equal(t, createdAt, response.Images[0].Created)
	assert.Equal(t, "sha256:abc123", response.Images[0].ID)
	assert.False(t, response.Images[0].Dangling)
	assert.True(t, response.Images[1].Dangling)
}

func TestHandler_ImagesPrune_AcceptsOptionalKeepLast(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	tests := []struct {
		name             string
		body             string
		expectedKeepLast int
	}{
		{name: "missing keep_last uses default", body: `{}`, expectedKeepLast: domain.DefaultImagePruneKeepLast},
		{name: "provided keep_last", body: `{"keep_last": 3}`, expectedKeepLast: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			imageSvc := &stubImageService{
				pruneFunc: func(_ context.Context, opts domain.ImagePruneOptions) (domain.ImagePruneReport, error) {
					called = true
					assert.Equal(t, tt.expectedKeepLast, opts.KeepLast)
					return domain.ImagePruneReport{
						Runtime:  domain.RuntimePruneResult{DeletedCount: 2, SpaceReclaimed: 4096},
						Registry: domain.RegistryPruneResult{TagsRemoved: 1, BlobsRemoved: 3, SpaceReclaimed: 2048},
					}, nil
				},
			}

			handler := newTestHandler(t, func(d *HandlerDeps) {
				d.ConfigSvc = configSvc
				d.AuthSvc = authSvc
				d.ContainerSvc = containerSvc
				d.SecretSvc = secretSvc
				d.ImageSvc = imageSvc
			})

			req := httptest.NewRequest("POST", "/admin/images/prune", bytes.NewBufferString(tt.body))
			req = req.WithContext(ctxWithScopes("admin:config:write"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.True(t, called)
			assert.Contains(t, rec.Body.String(), "deleted_count")
			assert.Contains(t, rec.Body.String(), "tags_removed")
		})
	}
}

func TestHandler_Images_ErrorMappingForServiceFailures(t *testing.T) {
	t.Run("list failure maps to internal server error", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		authSvc := inmocks.NewMockAuthService(t)
		containerSvc := inmocks.NewMockContainerService(t)
		secretSvc := inmocks.NewMockSecretService(t)

		imageSvc := &stubImageService{
			listImagesFunc: func(context.Context) ([]domain.ImageInfo, error) {
				return nil, errors.New("runtime list failed: confidential details")
			},
		}

		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.AuthSvc = authSvc
			d.ContainerSvc = containerSvc
			d.SecretSvc = secretSvc
			d.ImageSvc = imageSvc
		})

		req := httptest.NewRequest("GET", "/admin/images", nil)
		req = req.WithContext(ctxWithScopes("admin:status:read"))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, rec.Body.String(), "failed to list images")
		assert.NotContains(t, rec.Body.String(), "confidential details")
	})

	t.Run("prune failure maps to internal server error", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		authSvc := inmocks.NewMockAuthService(t)
		containerSvc := inmocks.NewMockContainerService(t)
		secretSvc := inmocks.NewMockSecretService(t)

		imageSvc := &stubImageService{
			pruneFunc: func(context.Context, domain.ImagePruneOptions) (domain.ImagePruneReport, error) {
				return domain.ImagePruneReport{}, errors.New("prune failed: confidential details")
			},
		}

		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.AuthSvc = authSvc
			d.ContainerSvc = containerSvc
			d.SecretSvc = secretSvc
			d.ImageSvc = imageSvc
		})

		req := httptest.NewRequest("POST", "/admin/images/prune", bytes.NewBufferString(`{"keep_last": 2}`))
		req = req.WithContext(ctxWithScopes("admin:config:write"))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.Contains(t, rec.Body.String(), "failed to prune images")
		assert.NotContains(t, rec.Body.String(), "confidential details")
	})
}

func TestHandler_ImagesPrune_ValidationAndAvailability(t *testing.T) {
	t.Run("rejects negative keep_last", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		authSvc := inmocks.NewMockAuthService(t)
		containerSvc := inmocks.NewMockContainerService(t)
		secretSvc := inmocks.NewMockSecretService(t)

		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.AuthSvc = authSvc
			d.ContainerSvc = containerSvc
			d.SecretSvc = secretSvc
			d.ImageSvc = &stubImageService{}
		})

		req := httptest.NewRequest("POST", "/admin/images/prune", bytes.NewBufferString(`{"keep_last": -1}`))
		req = req.WithContext(ctxWithScopes("admin:config:write"))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Contains(t, rec.Body.String(), "keep_last must be")
	})

	t.Run("returns service unavailable when image service missing", func(t *testing.T) {
		configSvc := inmocks.NewMockConfigService(t)
		authSvc := inmocks.NewMockAuthService(t)
		containerSvc := inmocks.NewMockContainerService(t)
		secretSvc := inmocks.NewMockSecretService(t)

		handler := newTestHandler(t, func(d *HandlerDeps) {
			d.ConfigSvc = configSvc
			d.AuthSvc = authSvc
			d.ContainerSvc = containerSvc
			d.SecretSvc = secretSvc
		})

		req := httptest.NewRequest("GET", "/admin/images", nil)
		req = req.WithContext(ctxWithScopes("admin:status:read"))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "image service not available")
	})
}

func TestHandler_ImagesPrune_ScopeDefaults(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	tests := []struct {
		name                  string
		body                  string
		expectedPruneDangling bool
		expectedPruneRegistry bool
		expectedKeepLast      int
		wantStatus            int
	}{
		{
			name:                  "missing scope fields defaults to both true",
			body:                  `{}`,
			expectedPruneDangling: true,
			expectedPruneRegistry: true,
			expectedKeepLast:      domain.DefaultImagePruneKeepLast,
			wantStatus:            http.StatusOK,
		},
		{
			name:                  "explicit dangling only",
			body:                  `{"prune_dangling": true, "prune_registry": false}`,
			expectedPruneDangling: true,
			expectedPruneRegistry: false,
			expectedKeepLast:      domain.DefaultImagePruneKeepLast,
			wantStatus:            http.StatusOK,
		},
		{
			name:                  "explicit registry only",
			body:                  `{"prune_dangling": false, "prune_registry": true, "keep_last": 5}`,
			expectedPruneDangling: false,
			expectedPruneRegistry: true,
			expectedKeepLast:      5,
			wantStatus:            http.StatusOK,
		},
		{
			name:       "both scopes false returns 400",
			body:       `{"prune_dangling": false, "prune_registry": false}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			imageSvc := &stubImageService{
				pruneFunc: func(_ context.Context, opts domain.ImagePruneOptions) (domain.ImagePruneReport, error) {
					called = true
					assert.Equal(t, tt.expectedPruneDangling, opts.PruneDangling)
					assert.Equal(t, tt.expectedPruneRegistry, opts.PruneRegistry)
					assert.Equal(t, tt.expectedKeepLast, opts.KeepLast)
					return domain.ImagePruneReport{}, nil
				},
			}

			handler := newTestHandler(t, func(d *HandlerDeps) {
				d.ConfigSvc = configSvc
				d.AuthSvc = authSvc
				d.ContainerSvc = containerSvc
				d.SecretSvc = secretSvc
				d.ImageSvc = imageSvc
			})

			req := httptest.NewRequest("POST", "/admin/images/prune", bytes.NewBufferString(tt.body))
			req = req.WithContext(ctxWithScopes("admin:config:write"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			if tt.wantStatus == http.StatusOK {
				assert.True(t, called, "prune should have been called")
			}
			if tt.wantStatus == http.StatusBadRequest {
				assert.Contains(t, rec.Body.String(), "at least one prune scope")
			}
		})
	}
}

func TestHandler_Images_Authorization(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.ConfigSvc = configSvc
		d.AuthSvc = authSvc
		d.ContainerSvc = containerSvc
		d.SecretSvc = secretSvc
		d.ImageSvc = &stubImageService{}
	})

	t.Run("images list requires status:read", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/admin/images", nil)
		req = req.WithContext(ctxWithScopes("admin:config:write"))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "insufficient permissions for status:read")
	})

	t.Run("images prune requires config:write", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/admin/images/prune", bytes.NewBufferString(`{"keep_last": 1}`))
		req = req.WithContext(ctxWithScopes("admin:status:read"))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.Contains(t, rec.Body.String(), "insufficient permissions for config:write")
	})
}

func TestHandler_TLSStatus_RequiresStatusReadScope(t *testing.T) {
	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.PublicTLSSvc = inmocks.NewMockPublicTLSService(t)
	})

	server := newScopedTestServer(t, handler, "admin:config:write")
	resp, err := http.Get(server.URL + "/admin/tls/status")
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	assert.Contains(t, string(body), "insufficient permissions for status:read")
}

func TestHandler_TLSStatus_GETReturnsJSON(t *testing.T) {
	now := time.Now()
	tlsStatus := domain.PublicTLSStatus{
		ACMEEnabled:     true,
		ConfiguredMode:  domain.ACMEChallengeAuto,
		EffectiveMode:   domain.ACMEChallengeHTTP01,
		SelectionReason: "auto-selected",
		TokenSource:     domain.ACMETokenSourcePass,
		Certificates: []domain.ManagedCertificate{
			{
				ID:        "cert-1",
				Names:     []string{"example.com"},
				Challenge: domain.ACMEChallengeHTTP01,
				Status:    domain.TLSCertificateStatusValid,
				NotAfter:  now.Add(60 * 24 * time.Hour),
			},
		},
		Routes: []domain.TLSRouteCoverage{
			{
				Domain:       "example.com",
				Covered:      true,
				CoveredBy:    "cert-1",
				RequiredACME: true,
			},
		},
	}

	handler := newTestHandler(t, func(d *HandlerDeps) {
		fakeSvc := inmocks.NewMockPublicTLSService(t)
		fakeSvc.EXPECT().Status(mock.Anything).Return(tlsStatus)
		d.PublicTLSSvc = fakeSvc
	})

	server := newScopedTestServer(t, handler, "admin:status:read")
	resp, err := http.Get(server.URL + "/admin/tls/status")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body dto.TLSStatusResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	assert.True(t, body.ACMEEnabled)
	assert.Equal(t, "auto", body.ConfiguredMode)
	assert.Equal(t, "http-01", body.EffectiveMode)
	assert.Equal(t, "auto-selected", body.SelectionReason)
	assert.Equal(t, "pass", body.TokenSource)
	require.Len(t, body.Certificates, 1)
	assert.Equal(t, "cert-1", body.Certificates[0].ID)
	assert.Equal(t, "valid", body.Certificates[0].Status)
	assert.Equal(t, "http-01", body.Certificates[0].Challenge)
	require.Len(t, body.Routes, 1)
	assert.Equal(t, "example.com", body.Routes[0].Domain)
	assert.True(t, body.Routes[0].Covered)
}

func TestHandler_TLSStatus_POSTReturns405(t *testing.T) {
	handler := newTestHandler(t, func(d *HandlerDeps) {
		d.PublicTLSSvc = inmocks.NewMockPublicTLSService(t)
	})

	server := newScopedTestServer(t, handler, "admin:status:read", "admin:status:write")
	resp, err := http.Post(server.URL+"/admin/tls/status", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
}
