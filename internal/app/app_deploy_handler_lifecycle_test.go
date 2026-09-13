package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/http/admin"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// blockingHandlerDeployEngine implements the deployment-engine port the
// daemon-owned app service executes through. ExecuteDeploy reports the
// context it received and blocks until released, so request-cancellation
// independence is observable at the HTTP handler boundary.
type blockingHandlerDeployEngine struct {
	started chan context.Context
	release chan struct{}
}

func (e *blockingHandlerDeployEngine) StartDeploy(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
	op := domain.AppOperation{Op: "op-1", Kind: "deploy", App: "blog", InputRevision: "rev-1"}
	return &deployment.StartDeployResult{
		Claim: deployment.DeployClaim{App: "blog", Op: "op-1", Service: "web", Revision: "rev-1", Journal: op},
		Owned: true,
	}, nil
}

func (e *blockingHandlerDeployEngine) ExecuteDeploy(ctx context.Context, _ deployment.DeployClaim) (*deployment.DeployResult, error) {
	e.started <- ctx
	<-e.release
	return &deployment.DeployResult{Op: "op-1", App: "blog"}, nil
}

func (e *blockingHandlerDeployEngine) AbandonDeploy(context.Context, deployment.DeployClaim) error {
	return nil
}

func (e *blockingHandlerDeployEngine) Stop(context.Context, string, string) (*deployment.LifecycleResult, error) {
	return nil, nil
}

func (e *blockingHandlerDeployEngine) Start(context.Context, string, string) (*deployment.LifecycleResult, error) {
	return nil, nil
}

func (e *blockingHandlerDeployEngine) Restart(context.Context, string, string, string) (*deployment.LifecycleResult, error) {
	return nil, nil
}

func (e *blockingHandlerDeployEngine) Remove(context.Context, string, string) (*deployment.LifecycleResult, error) {
	return nil, nil
}

// TestAppDeployHandler_RequestCancellationDoesNotAbortExecution drives the
// admin deploy handler over the real daemon-owned AppServiceImpl and proves
// the request boundary is inert: the handler answers 202 with the persisted
// running journal promptly, and cancelling the request context afterwards
// never reaches the in-flight replacement. The handler owns no goroutine; the
// execution derives from the daemon context.
func TestAppDeployHandler_RequestCancellationDoesNotAbortExecution(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	running := domain.AppOperation{Op: "op-1", Kind: "deploy", App: "blog", InputRevision: "rev-1"}
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").Return(running, nil).Once()
	// Show resolves no app: the response carries the journal alone.
	store.EXPECT().AppExists(mock.Anything, "blog").Return(false, nil).Once()

	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }

	engine := &blockingHandlerDeployEngine{started: make(chan context.Context, 1), release: release}
	svc := apps.NewAppServiceImpl(store, engine, nil, zerowrap.Default()).WithDaemonContext(context.Background())
	t.Cleanup(func() {
		unblock()
		_ = svc.Shutdown(context.Background())
	})

	handler := admin.NewHandler(admin.HandlerDeps{Log: zerowrap.Default(), AppSvc: svc})

	reqCtx, cancelRequest := context.WithCancel(
		context.WithValue(context.Background(), domain.ContextKeyScopes, []string{"admin:apps:write"}))
	defer cancelRequest()
	req := httptest.NewRequest(http.MethodPost, "/admin/apps/blog/deploy", nil)
	req.Header.Set("Idempotency-Key", "key-1")
	req = req.WithContext(reqCtx)
	rec := httptest.NewRecorder()

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		handler.ServeHTTP(rec, req)
	}()

	var execCtx context.Context
	select {
	case execCtx = <-engine.started:
	case <-time.After(3 * time.Second):
		t.Fatal("background deploy execution never started")
	}
	select {
	case <-serveDone:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not answer promptly")
	}
	require.Equal(t, http.StatusAccepted, rec.Code)
	// The persisted journal is what the handler answers with: the client can
	// poll the same op through operations/by-key.
	var resp dto.AppDeployResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "op-1", resp.Op)
	assert.Equal(t, dto.AppStatusRunning, resp.Status)

	// The request is over and its context is cancelled: the daemon-owned
	// execution must be unaffected.
	cancelRequest()
	select {
	case <-execCtx.Done():
		t.Fatal("request cancellation aborted the daemon-owned execution")
	case <-time.After(50 * time.Millisecond):
	}
}
