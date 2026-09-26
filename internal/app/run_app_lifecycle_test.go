package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/container"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// daemonCtxKey tags the daemon/supervisor lifecycle context so a test can
// prove the execution context descends from it and not from a request.
type daemonCtxKey struct{}

// fakeAppDeployEngine implements the deployment-engine port used by
// newAppDaemonService. ExecuteDeploy reports the context it received and
// blocks until that context ends, so cancellation is observable.
type fakeAppDeployEngine struct {
	executeStarted chan context.Context
}

func (f *fakeAppDeployEngine) StartDeploy(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
	return &deployment.StartDeployResult{
		Owned: true,
		Claim: deployment.DeployClaim{
			App: "blog", Op: "op-1", Service: "web", Revision: "rev-1",
			Journal: domain.AppOperation{Op: "op-1", Kind: "deploy", App: "blog", InputRevision: "rev-1"},
		},
	}, nil
}

func (f *fakeAppDeployEngine) ExecuteDeploy(ctx context.Context, _ deployment.DeployClaim) (*deployment.DeployResult, error) {
	f.executeStarted <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *fakeAppDeployEngine) AbandonDeploy(context.Context, deployment.DeployClaim) error {
	return nil
}

func (f *fakeAppDeployEngine) Stop(context.Context, string, string) (*deployment.LifecycleResult, error) {
	return nil, nil
}

func (f *fakeAppDeployEngine) Start(context.Context, string, string) (*deployment.LifecycleResult, error) {
	return nil, nil
}

func (f *fakeAppDeployEngine) Restart(context.Context, string, string, string) (*deployment.LifecycleResult, error) {
	return nil, nil
}

func (f *fakeAppDeployEngine) Remove(context.Context, string, string) (*deployment.LifecycleResult, error) {
	return nil, nil
}

// TestNewAppDaemonService_DaemonCancellationReachesInFlightDeploy proves the
// bootstrap wiring used by initApps: the app service derives background
// executions from the daemon/supervisor context, that context — not the
// request context — owns the in-flight replacement, and daemon cancellation
// reaches it.
func TestNewAppDaemonService_DaemonCancellationReachesInFlightDeploy(t *testing.T) {
	store := outmocks.NewMockAppState(t)
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").
		Return(domain.AppOperation{Op: "op-1", Kind: "deploy", App: "blog", InputRevision: "rev-1"}, nil).Maybe()

	engine := &fakeAppDeployEngine{executeStarted: make(chan context.Context, 1)}
	daemonCtx, cancelDaemon := context.WithCancel(context.WithValue(context.Background(), daemonCtxKey{}, "daemon"))
	svc := newAppDaemonService(daemonCtx, store, engine, nil, zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()

	deployDone := make(chan struct{})
	go func() {
		defer close(deployDone)
		_, _ = svc.Deploy(requestCtx, "blog", "rev-1", "web", false, "key-1")
	}()

	var execCtx context.Context
	select {
	case execCtx = <-engine.executeStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("background deploy execution never started")
	}
	assert.Equal(t, "daemon", execCtx.Value(daemonCtxKey{}), "execution must run on the daemon context")

	// The request ending is unrelated to the daemon-owned execution.
	cancelRequest()
	select {
	case <-execCtx.Done():
		t.Fatal("request cancellation aborted the daemon-owned execution")
	case <-time.After(50 * time.Millisecond):
	}

	cancelDaemon()
	select {
	case <-execCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("daemon context cancellation did not reach the in-flight execution")
	}
	<-deployDone
}

// orderRecordingAdmin records when app administration quiescence runs and
// on which context, so shutdown ordering is observable.
type orderRecordingAdmin struct {
	order    *[]string
	deadline bool
	err      error
}

func (a *orderRecordingAdmin) Shutdown(ctx context.Context) error {
	*a.order = append(*a.order, "app-shutdown")
	_, a.deadline = ctx.Deadline()
	return a.err
}

// TestGracefulShutdown_FailsClosedWhenAppAdministrationDoesNotQuiesce proves
// the fail-closed shutdown ordering: when app administration cannot unwind on
// the bounded shutdown context, the remaining state and runtime teardown is
// skipped and the error is returned for the process to exit non-zero, so an
// in-flight execution can never write to a closed store.
func TestGracefulShutdown_FailsClosedWhenAppAdministrationDoesNotQuiesce(t *testing.T) {
	// Keep internal-credential cleanup inside the test temp dir.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	order := []string{}
	admin := &orderRecordingAdmin{order: &order, err: errors.New("in-flight execution did not unwind")}

	store := outmocks.NewMockAppState(t)

	containerSvc := container.NewService(nil, nil, nil, container.Config{})
	err := gracefulShutdown(nil, nil, nil, containerSvc, nil, nil, nil, nil, nil, admin, store, zerowrap.Default())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "quiescence")
	require.Equal(t, []string{"app-shutdown"}, order, "state teardown must not run under an unfinished execution")
	store.AssertNotCalled(t, "Close")
	assert.True(t, admin.deadline, "app administration shutdown must run on the bounded shutdown context")
}

// TestGracefulShutdown_QuiescesAppAdministrationBeforeClosingState keeps the
// normal ordering: app administration is cancelled and joined on the bounded
// shutdown context before the app state store closes.
func TestGracefulShutdown_QuiescesAppAdministrationBeforeClosingState(t *testing.T) {
	// Keep internal-credential cleanup inside the test temp dir.
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	order := []string{}
	admin := &orderRecordingAdmin{order: &order}

	store := outmocks.NewMockAppState(t)
	store.EXPECT().Close().Run(func() { order = append(order, "state-close") }).Return(nil)

	containerSvc := container.NewService(nil, nil, nil, container.Config{})
	require.NoError(t, gracefulShutdown(nil, nil, nil, containerSvc, nil, nil, nil, nil, nil, admin, store, zerowrap.Default()))

	require.Equal(t, []string{"app-shutdown", "state-close"}, order)
	assert.True(t, admin.deadline, "app administration shutdown must run on the bounded shutdown context")
}
