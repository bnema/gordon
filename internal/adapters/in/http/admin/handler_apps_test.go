package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
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

// TestHandler_AppDeploy_MapsCleanupWarnings proves bounded leftovers of a
// successful mutation reach the wire instead of being dropped.
func TestHandler_AppDeploy_MapsCleanupWarnings(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	op := &domain.AppOperation{
		Op: "op-1", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Outcome: domain.AppOutcomeSuccess,
		Steps:   []domain.AppOperationStep{{ID: "service.web.replace", State: domain.AppStepSucceeded}},
		Warnings: []domain.AppOperationWarning{{
			Service: "web", Leftover: "ctr-old", Detail: "remove: still present",
		}},
	}
	appSvc.EXPECT().Deploy(mock.Anything, "blog", "", "", "test-operation-key").Return(op, nil).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{App: "blog"}, nil).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/deploy",
		dto.AppDeployRequest{}, "admin:apps:write")
	require.Equal(t, http.StatusOK, rec.Code)
	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.CleanupWarnings, 1)
	assert.Equal(t, "web", resp.CleanupWarnings[0].Service)
	assert.Equal(t, "ctr-old", resp.CleanupWarnings[0].Leftover)
	assert.Equal(t, "remove: still present", resp.CleanupWarnings[0].Detail)
}

// TestHandler_AppDeploy_RunningReturns202 proves a newly owned deploy whose
// effects are still executing answers 202 Accepted with the durable
// operation identity and a stable running status, so the client can recover
// completion through GET operations/by-key. The claim and its journal are
// already persisted when the handler answers.
func TestHandler_AppDeploy_RunningReturns202(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	running := &domain.AppOperation{
		Op: "op-run", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Steps: []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{ID: "service.web.replace", State: domain.AppStepPending},
		},
	}
	appSvc.EXPECT().Deploy(mock.Anything, "blog", "", "", "test-operation-key").Return(running, nil).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{App: "blog", Services: map[string]in.AppServiceView{}}, nil).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/deploy",
		dto.AppDeployRequest{}, "admin:apps:write")
	require.Equal(t, http.StatusAccepted, rec.Code)

	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "op-run", resp.Op)
	assert.Equal(t, "running", resp.Status)
	assert.Empty(t, resp.Outcome, "a running operation has no terminal outcome")
	require.Contains(t, resp.Services, "web")
	assert.Equal(t, "pending", resp.Services["web"].Result)
}

// TestHandler_AppDeploy_TerminalReplayReturnsOK proves a duplicate idempotency
// key whose stored operation already reached a successful terminal outcome
// replays with 200 and the stored status instead of 202.
func TestHandler_AppDeploy_TerminalReplayReturnsOK(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	terminal := &domain.AppOperation{
		Op: "op-done", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Outcome: domain.AppOutcomeSuccess,
		Steps:   []domain.AppOperationStep{{ID: "service.web.replace", State: domain.AppStepSucceeded}},
	}
	appSvc.EXPECT().Deploy(mock.Anything, "blog", "", "", "test-operation-key").Return(terminal, nil).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{App: "blog", Services: map[string]in.AppServiceView{}}, nil).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/deploy",
		dto.AppDeployRequest{}, "admin:apps:write")
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "op-done", resp.Op)
	assert.Equal(t, "success", resp.Status)
	assert.Equal(t, "success", resp.Outcome)
}

// TestHandler_AppDeploy_TerminalFailureReplayIsVisible proves a replay of a
// failed operation keeps its conflict mapping while still surfacing the
// stored failure and per-service result, never a 202.
func TestHandler_AppDeploy_TerminalFailureReplayIsVisible(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	failed := &domain.AppOperation{
		Op: "op-fail", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Outcome: domain.AppOutcomeFailed,
		Steps: []domain.AppOperationStep{{
			ID: "service.web.replace", State: domain.AppStepFailed, Error: "boom",
		}},
	}
	replayErr := fmt.Errorf("deployment: operation op-fail previously ended with outcome failed: %w", domain.ErrAppStateConflict)
	appSvc.EXPECT().Deploy(mock.Anything, "blog", "", "", "test-operation-key").Return(failed, replayErr).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{App: "blog", Services: map[string]in.AppServiceView{}}, nil).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/deploy",
		dto.AppDeployRequest{}, "admin:apps:write")
	require.Equal(t, http.StatusConflict, rec.Code)

	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "op-fail", resp.Op)
	assert.Equal(t, "failed", resp.Status)
	assert.Equal(t, "failed", resp.Outcome)
	require.Contains(t, resp.Services, "web")
	assert.Equal(t, "failed", resp.Services["web"].Result)
	assert.Equal(t, "boom", resp.Services["web"].Error)
}

// assertRunningReplayJournal pins the replay contract shared by a running
// replay before and after any service step has started: 409, the operation
// journal DTO with a running status and no terminal outcome, and never the
// mapped preflight error envelope. The top-level key set is asserted so both
// cases are proven to share one wire shape.
func assertRunningReplayJournal(t *testing.T, rec *httptest.ResponseRecorder, opID, lastStep string) {
	t.Helper()
	require.Equal(t, http.StatusConflict, rec.Code)

	var shape map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &shape))
	_, hasError := shape["error"]
	assert.False(t, hasError, "a running replay must not degrade to the preflight error envelope")
	keys := make([]string, 0, len(shape))
	for key := range shape {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	assert.Equal(t,
		[]string{"app", "effective", "op", "outcome", "retained", "revision", "services", "status", "steps"},
		keys, "a running replay always returns the operation journal shape")

	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, opID, resp.Op)
	assert.Equal(t, "running", resp.Status)
	assert.Empty(t, resp.Outcome, "a running operation has no terminal outcome")
	found := false
	for _, step := range resp.Steps {
		if step.ID == lastStep {
			found = true
			assert.Equal(t, domain.AppStepPending, step.State)
		}
	}
	assert.True(t, found, "the journal must expose step %q", lastStep)
}

// TestHandler_AppDeploy_RunningReplayBeforeServiceSteps proves a same-key
// replay of an operation still in flight answers 409 with the stored journal
// even before any service step has started. A non-terminal claim is a running
// journal, not a preflight failure, so the mapped error envelope never
// replaces it.
func TestHandler_AppDeploy_RunningReplayBeforeServiceSteps(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	running := &domain.AppOperation{
		Op: "op-run-pre", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Steps: []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepPending}},
	}
	replayErr := fmt.Errorf("deployment: operation op-run-pre has not reached a terminal outcome: %w", domain.ErrAppStateConflict)
	appSvc.EXPECT().Deploy(mock.Anything, "blog", "", "", "test-operation-key").Return(running, replayErr).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{App: "blog", Services: map[string]in.AppServiceView{}}, nil).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/deploy",
		dto.AppDeployRequest{}, "admin:apps:write")

	assertRunningReplayJournal(t, rec, "op-run-pre", "preflight")
}

// TestHandler_AppDeploy_RunningReplayAfterServiceSteps is the after-steps
// twin: the same running replay, now with a pending service step, must answer
// the identical 409 journal shape. Only the journal contents may differ from
// the before-steps case, never the envelope.
func TestHandler_AppDeploy_RunningReplayAfterServiceSteps(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	running := &domain.AppOperation{
		Op: "op-run-post", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Steps: []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{ID: "service.web.replace", State: domain.AppStepPending},
		},
	}
	replayErr := fmt.Errorf("deployment: operation op-run-post has not reached a terminal outcome: %w", domain.ErrAppStateConflict)
	appSvc.EXPECT().Deploy(mock.Anything, "blog", "", "", "test-operation-key").Return(running, replayErr).Once()
	appSvc.EXPECT().Show(mock.Anything, "blog").Return(&in.AppDetail{App: "blog", Services: map[string]in.AppServiceView{}}, nil).Once()

	rec := appsRequest(t, handler, http.MethodPost, "/admin/apps/blog/deploy",
		dto.AppDeployRequest{}, "admin:apps:write")

	assertRunningReplayJournal(t, rec, "op-run-post", "service.web.replace")
}

// TestHandler_AppDeploy_RejectsMissingIdempotencyKey keeps the request
// validation contract: a deploy without a valid key never reaches the
// service.
func TestHandler_AppDeploy_RejectsMissingIdempotencyKey(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	req := httptest.NewRequest(http.MethodPost, "/admin/apps/blog/deploy", nil)
	req = req.WithContext(ctxWithScopes("admin:apps:write"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
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

func TestHandler_AppSecretsList_MissingAppIs404(t *testing.T) {
	appSvc := inmocks.NewMockAppService(t)
	handler := appsTestHandler(t, appSvc)

	appSvc.EXPECT().ListSecrets(mock.Anything, "ghost", "").Return(
		nil,
		fmt.Errorf("apps: app %q does not exist: %w", "ghost", domain.ErrAppNotFound),
	).Once()

	rec := appsRequest(t, handler, http.MethodGet, "/admin/apps/ghost/secrets", nil, "admin:apps:read")
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "app-not-found")
}
