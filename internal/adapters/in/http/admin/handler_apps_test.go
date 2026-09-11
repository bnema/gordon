package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	in "github.com/bnema/gordon/internal/boundaries/in"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func appsTestHandler(t *testing.T, appSvc *inmocks.MockAppService) *Handler {
	t.Helper()
	return NewHandler(HandlerDeps{
		ConfigSvc:     inmocks.NewMockConfigService(t),
		AuthSvc:       inmocks.NewMockAuthService(t),
		ContainerSvc:  inmocks.NewMockContainerService(t),
		HealthSvc:     inmocks.NewMockHealthService(t),
		SecretSvc:     inmocks.NewMockSecretService(t),
		Log:           testLogger(),
		ReloadTrigger: noopReloadTrigger{},
		AppSvc:        appSvc,
	})
}

func appsRequest(t *testing.T, handler *Handler, method, target string, body any, scopes ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader)
	req = req.WithContext(ctxWithScopes(scopes...))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

const validAppManifest = `
name = "blog"
[[service]]
name = "web"
image = "img:1"
[[service.http]]
host = "blog.example.com"
port = 8080
`

func TestHandler_AppApply_Persists(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, false).Return(
		&in.AppApplyResult{App: "blog", ResultingRevision: "rev-1", Pending: true, IntentID: "apply-1"},
		nil, nil,
	).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/apply",
		dto.AppApplyRequest{ManifestTOML: validAppManifest}, "admin:apps:write")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AppApplyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "blog", resp.App)
	assert.Equal(t, "rev-1", resp.ResultingRevision)
	assert.True(t, resp.Pending)
}

func TestHandler_AppApply_RejectsBadManifest(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/apply",
		dto.AppApplyRequest{ManifestTOML: "name = \"Bad!\"\n"}, "admin:apps:write")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var envelope dto.AppError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "invalid-manifest", envelope.Error)
}

func TestHandler_AppDeploy_MapsOp(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	op := &domain.AppOperation{
		Op: "op-1", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Outcome: domain.AppOutcomeSuccess,
		Steps: []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{ID: "service.web.replace", State: domain.AppStepSucceeded, Before: "c-old", After: "c-new"},
		},
	}
	appSvc.EXPECT().Deploy(mock.Anything, "blog", "", "").Return(op, nil).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/deploy",
		dto.AppDeployRequest{}, "admin:apps:write")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "op-1", resp.Op)
	assert.Equal(t, "success", resp.Outcome)
	require.Contains(t, resp.Services, "web")
	assert.Equal(t, "deployed", resp.Services["web"].Result)
	assert.Equal(t, "c-new", resp.Services["web"].After)
}

func TestHandler_AppList_RequiresScope(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	rec := appsRequest(t, handler, http.MethodGet, "/admin/apps", nil, "admin:status:read")
	assert.Equal(t, http.StatusForbidden, rec.Code)

	appSvc.EXPECT().List(mock.Anything).Return(nil, nil).Once()
	rec = appsRequest(t, handler, http.MethodGet, "/admin/apps", nil, "admin:apps:read")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_AppSecretsSet_Forwards(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().SetSecrets(mock.Anything, "blog", "web", map[string]string{"DATABASE_URL": "v"}).Return(nil).Once()
	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/secrets/set",
		dto.AppSecretSetRequest{Service: "web", Secrets: map[string]string{"DATABASE_URL": "v"}}, "admin:apps:write")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_AppOpLookup_Recovers(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	op := &domain.AppOperation{Op: "op-9", Kind: "deploy", App: "blog", InputRevision: "rev-1", Outcome: "success"}
	appSvc.EXPECT().OperationByKey(mock.Anything, "blog", "op-9").Return(op, nil).Once()
	rec := appsRequest(t, handler, http.MethodGet, "/admin/apps/blog/operations/by-key/op-9", nil, "admin:apps:read")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "op-9", resp.Op)
}

// TestHandler_AppOpLookup_GatesDiagnosticsByLogsScope proves failure
// diagnostics are returned only to callers holding the logs read scope; an
// apps-read actor receives the stable error alone.
func TestHandler_AppOpLookup_GatesDiagnosticsByLogsScope(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	op := &domain.AppOperation{
		Op: "op-9", Kind: "deploy", App: "blog", InputRevision: "rev-1", Outcome: "failed",
		Steps: []domain.AppOperationStep{{
			ID: "service.web.replace", State: domain.AppStepFailed,
			Error: "deployment: readiness failed", Diagnostics: []string{"SECRET=supersecret"},
		}},
	}
	appSvc.EXPECT().OperationByKey(mock.Anything, "blog", "op-9").Return(op, nil).Twice()

	appsRead := appsRequest(t, handler, http.MethodGet, "/admin/apps/blog/operations/by-key/op-9", nil, "admin:apps:read")
	require.Equal(t, http.StatusOK, appsRead.Code)
	assert.NotContains(t, appsRead.Body.String(), "supersecret", "apps-read must not receive application diagnostics")
	assert.NotContains(t, appsRead.Body.String(), "diagnostics")

	logsRead := appsRequest(t, handler, http.MethodGet, "/admin/apps/blog/operations/by-key/op-9",
		nil, "admin:apps:read", "admin:logs:read")
	require.Equal(t, http.StatusOK, logsRead.Code)
	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(logsRead.Body.Bytes(), &resp))
	require.Len(t, resp.Steps, 1)
	require.Len(t, resp.Steps[0].Diagnostics, 1)
	assert.Equal(t, "SECRET=supersecret", resp.Steps[0].Diagnostics[0])
}

// TestHandler_AppApply_DryRunOmitsRevision proves a dry-run apply never
// reports a revision: nothing was persisted, so resulting_revision is
// omitted instead of echoing the app name.
func TestHandler_AppApply_DryRunOmitsRevision(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, true).Return(
		nil,
		&in.AppDryRunResult{App: "blog", Valid: true},
		nil,
	).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/apply",
		dto.AppApplyRequest{ManifestTOML: validAppManifest, DryRun: true}, "admin:apps:write")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AppApplyResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "blog", resp.App)
	assert.Empty(t, resp.ResultingRevision)
	assert.NotContains(t, rec.Body.String(), "resulting_revision")
}
