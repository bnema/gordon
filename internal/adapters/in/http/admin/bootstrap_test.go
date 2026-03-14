package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestBootstrap_FullWorkflow_HappyPath(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, registrySvc, nil, testLogger(), nil)

	payload := dto.BootstrapRequest{
		Domain:      "app.example.com",
		Image:       "myapp:latest",
		Attachments: []string{"postgres:16"},
		Env:         map[string]string{"APP_ENV": "prod"},
		AttachmentEnv: map[string]map[string]string{
			"postgres": {"POSTGRES_PASSWORD": "secret"},
		},
	}

	configSvc.EXPECT().GetRegistryDomain().Return("reg.bnema.dev").Once()
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{Domain: payload.Domain, Image: "reg.bnema.dev/myapp"}).Return(nil).Once()
	configSvc.EXPECT().AddAttachment(mock.Anything, payload.Domain, "postgres:16").Return(nil).Once()
	secretSvc.EXPECT().Set(mock.Anything, payload.Domain, map[string]string{"APP_ENV": "prod"}).Return(nil).Once()
	secretSvc.EXPECT().SetAttachment(mock.Anything, payload.Domain, "postgres", map[string]string{"POSTGRES_PASSWORD": "secret"}).Return(nil).Once()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", bytes.NewReader(body))
	req = req.WithContext(ctxWithScopes("admin:routes:write", "admin:secrets:write", "admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	registrySvc.AssertNotCalled(t, "GetManifest", mock.Anything, mock.Anything, mock.Anything)

	var response dto.BootstrapResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, payload.Domain, response.Domain)
	assert.Equal(t, "reg.bnema.dev/myapp", response.Image)
	assert.Equal(t, "push reg.bnema.dev/myapp to trigger deployment", response.Next)
	assert.Equal(t, []dto.BootstrapStep{
		{Name: "route", Status: "configured"},
		{Name: "attachment:postgres:16", Status: "created"},
		{Name: "env", Status: "updated"},
		{Name: "attachment_env:postgres", Status: "updated"},
	}, response.Steps)
}

func TestBootstrap_Idempotent_Rerun(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	payload := dto.BootstrapRequest{
		Domain:      "app.example.com",
		Image:       "myapp:latest",
		Attachments: []string{"postgres:16"},
		Env:         map[string]string{"APP_ENV": "prod"},
		AttachmentEnv: map[string]map[string]string{
			"postgres": {"POSTGRES_PASSWORD": "secret"},
		},
	}

	configSvc.EXPECT().GetRegistryDomain().Return("reg.bnema.dev").Times(2)
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{Domain: payload.Domain, Image: "reg.bnema.dev/myapp"}).Return(nil).Once()
	configSvc.EXPECT().AddAttachment(mock.Anything, payload.Domain, "postgres:16").Return(nil).Once()
	secretSvc.EXPECT().Set(mock.Anything, payload.Domain, map[string]string{"APP_ENV": "prod"}).Return(nil).Once()
	secretSvc.EXPECT().SetAttachment(mock.Anything, payload.Domain, "postgres", map[string]string{"POSTGRES_PASSWORD": "secret"}).Return(nil).Once()

	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{Domain: payload.Domain, Image: "reg.bnema.dev/myapp"}).Return(nil).Once()
	configSvc.EXPECT().AddAttachment(mock.Anything, payload.Domain, "postgres:16").Return(domain.ErrAttachmentExists).Once()
	secretSvc.EXPECT().Set(mock.Anything, payload.Domain, map[string]string{"APP_ENV": "prod"}).Return(nil).Once()
	secretSvc.EXPECT().SetAttachment(mock.Anything, payload.Domain, "postgres", map[string]string{"POSTGRES_PASSWORD": "secret"}).Return(nil).Once()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	for i := range 2 {
		req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", bytes.NewReader(body))
		req = req.WithContext(ctxWithScopes("admin:routes:write", "admin:secrets:write", "admin:config:write"))
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)

		var response dto.BootstrapResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
		assert.Equal(t, payload.Domain, response.Domain)
		assert.Equal(t, "reg.bnema.dev/myapp", response.Image)
		require.Len(t, response.Steps, 4)
		if i == 0 {
			assert.Equal(t, []dto.BootstrapStep{
				{Name: "route", Status: "configured"},
				{Name: "attachment:postgres:16", Status: "created"},
				{Name: "env", Status: "updated"},
				{Name: "attachment_env:postgres", Status: "updated"},
			}, response.Steps)
		} else {
			assert.Equal(t, []dto.BootstrapStep{
				{Name: "route", Status: "configured"},
				{Name: "attachment:postgres:16", Status: "noop"},
				{Name: "env", Status: "updated"},
				{Name: "attachment_env:postgres", Status: "updated"},
			}, response.Steps)
		}
	}
}

func TestBootstrap_MissingDomain(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	body, err := json.Marshal(dto.BootstrapRequest{Image: "myapp:latest"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", bytes.NewReader(body))
	req = req.WithContext(ctxWithScopes("admin:routes:write", "admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"error":"domain is required"}`+"\n", rec.Body.String())
}

func TestBootstrap_MissingImage(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	body, err := json.Marshal(dto.BootstrapRequest{Domain: "app.example.com"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", bytes.NewReader(body))
	req = req.WithContext(ctxWithScopes("admin:routes:write", "admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"error":"image is required"}`+"\n", rec.Body.String())
}

func TestBootstrap_NormalizesImageBeforeStoring(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	payload := dto.BootstrapRequest{Domain: "app.example.com", Image: "pitlane:v1"}
	configSvc.EXPECT().GetRegistryDomain().Return("reg.bnema.dev").Once()
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{Domain: payload.Domain, Image: "reg.bnema.dev/pitlane"}).Return(nil).Once()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", bytes.NewReader(body))
	req = req.WithContext(ctxWithScopes("admin:routes:write", "admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response dto.BootstrapResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, payload.Domain, response.Domain)
	assert.Equal(t, "reg.bnema.dev/pitlane", response.Image)
	assert.Equal(t, "push reg.bnema.dev/pitlane to trigger deployment", response.Next)
}

func TestBootstrap_AddRouteValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		mockErr error
	}{
		{
			name:    "empty domain from service",
			mockErr: domain.ErrRouteDomainEmpty,
		},
		{
			name:    "empty image from service",
			mockErr: domain.ErrRouteImageEmpty,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configSvc := inmocks.NewMockConfigService(t)
			authSvc := inmocks.NewMockAuthService(t)
			containerSvc := inmocks.NewMockContainerService(t)
			secretSvc := inmocks.NewMockSecretService(t)

			handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

			payload := dto.BootstrapRequest{Domain: "app.example.com", Image: "myapp:latest"}
			configSvc.EXPECT().GetRegistryDomain().Return("reg.bnema.dev").Once()
			configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{Domain: payload.Domain, Image: "reg.bnema.dev/myapp"}).Return(tt.mockErr).Once()

			body, err := json.Marshal(payload)
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", bytes.NewReader(body))
			req = req.WithContext(ctxWithScopes("admin:routes:write", "admin:config:write"))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.JSONEq(t, `{"error":"`+tt.mockErr.Error()+`"}`+"\n", rec.Body.String())
		})
	}
}

func TestBootstrap_PartialFailure_AttachmentError(t *testing.T) {
	configSvc := inmocks.NewMockConfigService(t)
	authSvc := inmocks.NewMockAuthService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	secretSvc := inmocks.NewMockSecretService(t)

	handler := NewHandler(configSvc, authSvc, containerSvc, inmocks.NewMockHealthService(t), secretSvc, nil, inmocks.NewMockRegistryService(t), nil, testLogger(), nil)

	payload := dto.BootstrapRequest{
		Domain:      "app.example.com",
		Image:       "myapp:latest",
		Attachments: []string{"postgres:16"},
	}

	configSvc.EXPECT().GetRegistryDomain().Return("reg.bnema.dev").Once()
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{Domain: payload.Domain, Image: "reg.bnema.dev/myapp"}).Return(nil).Once()
	configSvc.EXPECT().AddAttachment(mock.Anything, payload.Domain, "postgres:16").Return(errors.New("attachment failed")).Once()

	body, err := json.Marshal(payload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/admin/bootstrap", bytes.NewReader(body))
	req = req.WithContext(ctxWithScopes("admin:routes:write", "admin:config:write"))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var response dto.BootstrapResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, []dto.BootstrapStep{
		{Name: "route", Status: "configured"},
		{Name: "attachment:postgres:16", Status: "failed"},
	}, response.Steps)
	secretSvc.AssertNotCalled(t, "Set", mock.Anything, mock.Anything, mock.Anything)
	secretSvc.AssertNotCalled(t, "SetAttachment", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}
