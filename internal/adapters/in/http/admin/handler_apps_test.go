package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	if method != http.MethodGet {
		req.Header.Set("Idempotency-Key", "test-operation-key")
	}
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
	appSvc.EXPECT().Deploy(mock.Anything, "blog", "", "", "test-operation-key").Return(op, nil).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{
		App: "blog", Converged: true, ConvergedRevision: "rev-1",
		Services: map[string]in.AppServiceView{"web": {EffectiveRevision: "rev-1", Container: "c-new"}},
		Retained: in.AppRetainedView{Volumes: []string{"gordon-blog--web--vol--data"}, Secrets: []string{"gordon/apps/app-1/web/database-url"}},
	}, nil).Once()

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
	// Effective and retained state comes from the app read model, never
	// from the journal outcome.
	require.NotNil(t, resp.Effective)
	assert.True(t, resp.Effective.Converged)
	assert.Equal(t, "rev-1", resp.Effective.Services["web"])
	require.NotNil(t, resp.Retained)
	assert.Equal(t, []string{"gordon-blog--web--vol--data"}, resp.Retained.Volumes)
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

// TestHandler_AppLifecycle_UnknownAppIsNotFound proves a lifecycle
// mutation of a name with no app identity is a 404, not a 500 or a silent
// success.
func TestHandler_AppLifecycle_UnknownAppIsNotFound(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().Stop(mock.Anything, "ghost", "test-operation-key").
		Return(nil, fmt.Errorf("deployment: app %q does not exist: %w", "ghost", domain.ErrAppNotFound)).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/ghost/stop", nil, "admin:apps:write")
	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope dto.AppError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "app-not-found", envelope.Error)
}

// TestHandler_AppLifecycle_ReplayConflictCarriesJournal proves a replayed
// key that never finished returns 409 with the stored journal, so the
// caller can inspect what was recorded instead of guessing.
func TestHandler_AppLifecycle_ReplayConflictCarriesJournal(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	op := &domain.AppOperation{
		Op: "test-operation-key", Kind: "remove", App: "blog",
		Steps: []domain.AppOperationStep{{ID: "service.web.remove", State: domain.AppStepPending}},
	}
	appSvc.EXPECT().Remove(mock.Anything, "blog", "test-operation-key").
		Return(op, domain.ErrAppStateConflict).Once()
	// A removal that already retired the incarnation has no app read
	// model left, so the response carries the journal alone.
	appSvc.EXPECT().Show(mock.Anything, "blog").
		Return(nil, domain.ErrAppNotFound).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/remove", nil, "admin:apps:write")
	require.Equal(t, http.StatusConflict, rec.Code)
	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "test-operation-key", resp.Op)
	assert.Nil(t, resp.Effective)
	assert.Nil(t, resp.Retained)
}

func TestHandler_AppShow_MapsReadModel(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{
		App:               "blog",
		DesiredRevision:   "rev-2",
		DesiredStatus:     "pending",
		Pending:           true,
		Converged:         false,
		ConvergedRevision: "rev-1",
		Stopped:           true,
		Services: map[string]in.AppServiceView{
			"web": {EffectiveRevision: "rev-1", Digest: "sha256:x", Container: "ctr-1", RestartUnsafe: true},
		},
		Retained: in.AppRetainedView{
			Volumes: []string{"gordon-blog--web--vol--data"},
			Secrets: []string{"gordon/apps/app-1/web/database-url"},
			Images:  []string{"registry.example.com/blog/web:1.4.2"},
		},
		LastOp:          "op-9",
		LastOpKind:      "deploy",
		LastOutcome:     "partial",
		LastOpStartedAt: started,
	}, nil).Once()

	rec := appsRequest(t, handler, http.MethodGet, "/admin/apps/blog", nil, "admin:apps:read")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AppShowResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Equal(t, "rev-2", resp.Desired.Revision)
	assert.True(t, resp.Desired.Pending)
	assert.False(t, resp.Active.Converged)
	assert.Equal(t, "rev-1", resp.Active.ConvergedRevision)
	assert.True(t, resp.Intent.Stopped)
	assert.True(t, resp.Active.Services["web"].RestartUnsafe, "restart safety must reach the wire")
	assert.Equal(t, "ctr-1", resp.Active.Services["web"].Container)
	assert.Equal(t, []string{"gordon-blog--web--vol--data"}, resp.Retained.Volumes)
	assert.Equal(t, []string{"registry.example.com/blog/web:1.4.2"}, resp.Retained.Images)
	require.NotNil(t, resp.LastOp)
	assert.Equal(t, "op-9", resp.LastOp.Op)
	assert.Equal(t, "deploy", resp.LastOp.Kind)
	assert.Equal(t, "partial", resp.LastOp.Outcome)
	assert.Equal(t, started, resp.LastOp.StartedAt)
}

// TestHandler_AppShow_UnknownAppIsNotFound proves showing a name with no
// app identity is 404, not an empty success.
func TestHandler_AppShow_UnknownAppIsNotFound(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().Show(mock.Anything, "ghost").
		Return(nil, fmt.Errorf("apps: app %q does not exist: %w", "ghost", domain.ErrAppNotFound)).Once()

	rec := appsRequest(t, handler, http.MethodGet, "/admin/apps/ghost", nil, "admin:apps:read")
	require.Equal(t, http.StatusNotFound, rec.Code)
	var envelope dto.AppError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "app-not-found", envelope.Error)
}

func TestHandler_AppList_MapsReadModel(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().List(mock.Anything).Return([]in.AppSummary{{
		App: "blog", Desired: "rev-2", DesiredStatus: "pending",
		Active: "rev-1", Converged: false, Pending: true, LastOutcome: "partial",
	}}, nil).Once()

	rec := appsRequest(t, handler, http.MethodGet, "/admin/apps", nil, "admin:apps:read")
	require.Equal(t, http.StatusOK, rec.Code)
	var envelope struct {
		Apps []dto.AppSummaryDTO `json:"apps"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.Len(t, envelope.Apps, 1)
	assert.Equal(t, "rev-2", envelope.Apps[0].Desired)
	assert.Equal(t, "pending", envelope.Apps[0].DesiredStatus)
	assert.True(t, envelope.Apps[0].Pending)
	assert.Equal(t, "partial", envelope.Apps[0].LastOutcome)
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
