package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
)

func retiredTestHandler(t *testing.T) *Handler {
	t.Helper()
	return NewHandler(HandlerDeps{
		ConfigSvc:     inmocks.NewMockConfigService(t),
		AuthSvc:       inmocks.NewMockAuthService(t),
		ContainerSvc:  inmocks.NewMockContainerService(t),
		HealthSvc:     inmocks.NewMockHealthService(t),
		SecretSvc:     inmocks.NewMockSecretService(t),
		Log:           testLogger(),
		ReloadTrigger: noopReloadTrigger{},
	})
}

func TestHandler_RetiredMutations_Return410(t *testing.T) {
	handler := retiredTestHandler(t)
	targets := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/admin/deploy/example.com"},
		{http.MethodPost, "/admin/restart/example.com"},
		{http.MethodPost, "/admin/deploy-intent"},
		{http.MethodPost, "/admin/routes"},
		{http.MethodGet, "/admin/routes"},
		{http.MethodDelete, "/admin/routes/example.com"},
		{http.MethodPost, "/admin/attachments"},
		{http.MethodGet, "/admin/attachments/by-image/x"},
		{http.MethodPost, "/admin/attachments/prune"},
		{http.MethodGet, "/admin/attachments/orphans"},
		{http.MethodPost, "/admin/bootstrap"},
		{http.MethodPost, "/admin/preview/blog"},
		{http.MethodGet, "/admin/previews"},
		{http.MethodPost, "/admin/autoroute/allowed-domains"},
	}
	for _, target := range targets {
		req := httptest.NewRequest(target.method, target.path, nil)
		req = req.WithContext(ctxWithScopes("admin:*:*"))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusGone, rec.Code, "%s %s", target.method, target.path)
		var envelope dto.AppError
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), target.path)
		assert.Equal(t, "endpoint-retired", envelope.Error, target.path)
	}
}
