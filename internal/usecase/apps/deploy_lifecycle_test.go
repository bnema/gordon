package apps_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/apps"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

type deployCall struct {
	op  *domain.AppOperation
	err error
}

// callDeploy runs Deploy off the test goroutine so a blocking execution can
// never hang the test: only a prompt return is awaited.
func callDeploy(t *testing.T, svc *apps.AppServiceImpl, ctx context.Context, key string) <-chan deployCall {
	t.Helper()
	out := make(chan deployCall, 1)
	go func() {
		op, err := svc.Deploy(ctx, "blog", "rev-1", "web", false, key)
		out <- deployCall{op: op, err: err}
	}()
	return out
}

func awaitDeploy(t *testing.T, ch <-chan deployCall) deployCall {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(3 * time.Second):
		t.Fatal("Deploy did not return promptly")
		return deployCall{}
	}
}

// gate returns a release channel and an idempotent closer, so a blocking test
// execution is always unblocked before Shutdown waits on it.
func gate() (chan struct{}, func()) {
	ch := make(chan struct{})
	var once sync.Once
	return ch, func() { once.Do(func() { close(ch) }) }
}

func runningDeployOp() domain.AppOperation {
	return domain.AppOperation{Op: "op-1", Kind: "deploy", App: "blog", InputRevision: "rev-1", StartedAt: time.Now().UTC()}
}

func ownedStart(op domain.AppOperation) *deployment.StartDeployResult {
	return &deployment.StartDeployResult{
		Claim: deployment.DeployClaim{App: op.App, Op: op.Op, Service: "web", Revision: "rev-1", Journal: op},
		Owned: true,
	}
}

func replayStart(op domain.AppOperation) *deployment.StartDeployResult {
	return &deployment.StartDeployResult{
		Claim: deployment.DeployClaim{App: op.App, Op: op.Op, Service: "web", Revision: "rev-1", Journal: op},
		Owned: false,
	}
}

// TestAppServiceImpl_Deploy_ReturnsRunningJournalAfterPersistence proves the
// request path is two-phase: StartDeploy has already journaled the operation
// when the background execution starts, and Deploy returns that non-terminal
// journal without waiting for the replacement.
func TestAppServiceImpl_Deploy_ReturnsRunningJournalAfterPersistence(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	op := runningDeployOp()
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").Return(op, nil)

	startInputs := make(chan deployment.DeployInput, 1)
	deploy.startDeployFn = func(_ context.Context, input deployment.DeployInput) (*deployment.StartDeployResult, error) {
		startInputs <- input
		return ownedStart(op), nil
	}

	persistedBeforeExecution := make(chan error, 1)
	release, releaseExecution := gate()
	t.Cleanup(releaseExecution)
	deploy.executeDeployFn = func(execCtx context.Context, claim deployment.DeployClaim) (*deployment.DeployResult, error) {
		// The claimed journal must be durable and queryable before any effect.
		stored, err := store.LoadOperation(execCtx, claim.App, claim.Op)
		if err == nil && (stored.Op != claim.Op || stored.Terminal()) {
			err = errors.New("claimed journal is not the persisted running operation")
		}
		persistedBeforeExecution <- err
		<-release
		return &deployment.DeployResult{Op: claim.Op, App: claim.App}, nil
	}

	res := awaitDeploy(t, callDeploy(t, svc, ctx, "key-1"))
	require.NoError(t, res.err)
	require.NotNil(t, res.op)
	assert.Equal(t, "op-1", res.op.Op)
	assert.False(t, res.op.Terminal(), "Deploy returns the still-running journal")
	assert.Equal(t, deployment.DeployInput{App: "blog", Revision: "rev-1", Service: "web", Op: "key-1"}, <-startInputs)

	select {
	case err := <-persistedBeforeExecution:
		require.NoError(t, err, "execution must start only after the journal is persisted")
	case <-time.After(3 * time.Second):
		t.Fatal("background execution never started")
	}
	releaseExecution()
}

// TestAppServiceImpl_Deploy_RequestCancellationDoesNotCancelExecution proves
// the execution runs on the daemon context, never on the request context.
func TestAppServiceImpl_Deploy_RequestCancellationDoesNotCancelExecution(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	op := runningDeployOp()
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").Return(op, nil)
	deploy.startDeployFn = func(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
		return ownedStart(op), nil
	}

	execCtxCh := make(chan context.Context, 1)
	release, releaseExecution := gate()
	t.Cleanup(releaseExecution)
	deploy.executeDeployFn = func(execCtx context.Context, _ deployment.DeployClaim) (*deployment.DeployResult, error) {
		execCtxCh <- execCtx
		<-release
		return nil, nil
	}

	res := awaitDeploy(t, callDeploy(t, svc, requestCtx, "key-1"))
	require.NoError(t, res.err)
	require.NotNil(t, res.op)

	execCtx := <-execCtxCh
	cancelRequest()
	select {
	case <-requestCtx.Done():
	default:
		t.Fatal("request context was not cancelled")
	}
	require.NotEqual(t, requestCtx, execCtx, "execution must not run on the request context")
	select {
	case <-execCtx.Done():
		t.Fatal("request cancellation cancelled the in-flight execution")
	case <-time.After(50 * time.Millisecond):
	}
	releaseExecution()
}

// TestAppServiceImpl_Deploy_DuplicateKeyNeverSpawnsSecondExecution proves the
// claimed key is the only execution: a replay returns the stored journal, an
// in-flight replay reports a conflict, and ExecuteDeploy runs exactly once.
func TestAppServiceImpl_Deploy_DuplicateKeyNeverSpawnsSecondExecution(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())
	t.Cleanup(func() { require.NoError(t, svc.Shutdown(context.Background())) })

	op := runningDeployOp()
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").Return(op, nil)

	var startCalls atomic.Int32
	deploy.startDeployFn = func(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
		if startCalls.Add(1) == 1 {
			return ownedStart(op), nil
		}
		return replayStart(op), nil
	}
	var execCalls atomic.Int32
	execStarted := make(chan struct{})
	release, releaseExecution := gate()
	t.Cleanup(releaseExecution)
	deploy.executeDeployFn = func(context.Context, deployment.DeployClaim) (*deployment.DeployResult, error) {
		execCalls.Add(1)
		close(execStarted)
		<-release
		return &deployment.DeployResult{Op: "op-1"}, nil
	}

	first := awaitDeploy(t, callDeploy(t, svc, ctx, "key-1"))
	require.NoError(t, first.err)
	require.NotNil(t, first.op)
	select {
	case <-execStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("first execution never started")
	}

	second := awaitDeploy(t, callDeploy(t, svc, ctx, "key-1"))
	require.ErrorIs(t, second.err, domain.ErrAppStateConflict, "an in-flight replay is never a second execution")
	require.NotNil(t, second.op)
	assert.Equal(t, "op-1", second.op.Op)
	assert.Equal(t, int32(1), execCalls.Load(), "a duplicate key never spawns a second execution")
	releaseExecution()
}

// TestAppServiceImpl_Deploy_TerminalFailureRemainsQueryable proves a failed
// execution is recorded once, stays readable by key, and its replay reports
// the stored failure without executing again.
func TestAppServiceImpl_Deploy_TerminalFailureRemainsQueryable(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())

	failed := runningDeployOp()
	failed.Outcome = domain.AppOutcomeFailed
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").Return(failed, nil)

	var startCalls atomic.Int32
	deploy.startDeployFn = func(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
		if startCalls.Add(1) == 1 {
			return ownedStart(runningDeployOp()), nil
		}
		return replayStart(failed), nil
	}
	var execCalls atomic.Int32
	executed := make(chan struct{}, 1)
	deploy.executeDeployFn = func(context.Context, deployment.DeployClaim) (*deployment.DeployResult, error) {
		execCalls.Add(1)
		executed <- struct{}{}
		return nil, errors.New("replacement failed")
	}

	res := awaitDeploy(t, callDeploy(t, svc, ctx, "key-1"))
	require.NoError(t, res.err, "a failed execution is reported through the journal, not the start call")
	require.NotNil(t, res.op)
	assert.True(t, res.op.Terminal())
	// Join the background execution before asserting the journal, so the
	// count is deterministic rather than a race with the goroutine.
	<-executed
	assert.Equal(t, int32(1), execCalls.Load())

	// A terminal replay stays queryable and never executes again, even
	// though it is answered as a conflict.
	retry := awaitDeploy(t, callDeploy(t, svc, ctx, "key-1"))
	require.ErrorIs(t, retry.err, domain.ErrAppStateConflict)
	require.NotNil(t, retry.op)
	assert.Equal(t, domain.AppOutcomeFailed, retry.op.Outcome)
	assert.Equal(t, int32(1), execCalls.Load(), "a terminal replay never executes again")

	require.NoError(t, svc.Shutdown(context.Background()))

	got, err := svc.OperationByKey(ctx, "blog", "op-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AppOutcomeFailed, got.Outcome, "terminal failure remains queryable by key")
}

// TestAppServiceImpl_Deploy_ShutdownLeavesRecoverableNonTerminalJournal proves
// graceful shutdown cancels the execution and leaves the journal persisted
// non-terminal for reconciliation, instead of losing or finalizing it.
func TestAppServiceImpl_Deploy_ShutdownLeavesRecoverableNonTerminalJournal(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default()).
		WithDaemonContext(context.Background())

	op := runningDeployOp()
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").Return(op, nil)
	deploy.startDeployFn = func(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
		return ownedStart(op), nil
	}

	execCtxCh := make(chan context.Context, 1)
	deploy.executeDeployFn = func(execCtx context.Context, _ deployment.DeployClaim) (*deployment.DeployResult, error) {
		execCtxCh <- execCtx
		<-execCtx.Done()
		return nil, execCtx.Err()
	}

	res := awaitDeploy(t, callDeploy(t, svc, ctx, "key-1"))
	require.NoError(t, res.err)
	require.NotNil(t, res.op)

	execCtx := <-execCtxCh
	require.NoError(t, svc.Shutdown(context.Background()), "Shutdown waits for the execution to unwind")
	require.ErrorIs(t, execCtx.Err(), context.Canceled, "shutdown cancels the in-flight execution")

	got, err := svc.OperationByKey(ctx, "blog", "op-1")
	require.NoError(t, err)
	assert.False(t, got.Terminal(), "shutdown must leave the non-terminal journal for reconciliation")
	assert.Equal(t, "op-1", got.Op)
}

// TestAppServiceImpl_Deploy_RefusesAfterShutdownBeforeClaim proves a deploy
// that arrives after shutdown began is refused with a state conflict before
// the engine can claim it, so neither a durable 202-running journal nor a
// workload effect can be produced. The engine mock has no expectations, so
// any StartDeploy or ExecuteDeploy call fails the test.
func TestAppServiceImpl_Deploy_RefusesAfterShutdownBeforeClaim(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())

	require.NoError(t, svc.Shutdown(context.Background()))

	res := awaitDeploy(t, callDeploy(t, svc, ctx, "key-1"))
	require.ErrorIs(t, res.err, domain.ErrAppStateConflict, "a deploy after shutdown is refused with a state conflict")
	require.Nil(t, res.op, "a refused deploy never produces a journal to answer 202")
}

// TestAppServiceImpl_Deploy_ShutdownRacingClaimSettlesWithoutExecuting proves
// the claim/schedule hand-off is closed against shutdown: once Shutdown starts
// after StartDeploy claimed the operation but before it was scheduled, the
// owned claim is settled without starting, Shutdown waits for that settle, and
// the request answers a conflict instead of a false 202-running orphan.
func TestAppServiceImpl_Deploy_ShutdownRacingClaimSettlesWithoutExecuting(t *testing.T) {
	ctx := context.Background()
	store := newMockAppState(t)
	deploy := newMockDeployEngine(t)
	svc := apps.NewAppServiceImpl(store, deploy, newMockSecretWriter(t), zerowrap.Default())

	op := runningDeployOp()
	store.EXPECT().LoadOperation(mock.Anything, "blog", "op-1").Return(op, nil)

	claimed := make(chan struct{})
	releaseClaim := make(chan struct{})
	deploy.startDeployFn = func(context.Context, deployment.DeployInput) (*deployment.StartDeployResult, error) {
		close(claimed)
		<-releaseClaim
		return ownedStart(op), nil
	}
	var execCalls atomic.Int32
	deploy.executeDeployFn = func(context.Context, deployment.DeployClaim) (*deployment.DeployResult, error) {
		execCalls.Add(1)
		return nil, nil
	}
	abandoned := make(chan deployment.DeployClaim, 1)
	deploy.abandonDeployFn = func(_ context.Context, claim deployment.DeployClaim) error {
		abandoned <- claim
		return nil
	}

	resCh := callDeploy(t, svc, ctx, "key-1")
	<-claimed

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- svc.Shutdown(context.Background()) }()

	// Shutdown must wait for the hand-off rather than return while the claim
	// is still being made.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned before the in-flight claim hand-off settled")
	case <-time.After(50 * time.Millisecond):
	}

	// The claim completes now; shutdown is already set, so it must be settled
	// rather than executed.
	close(releaseClaim)

	select {
	case claim := <-abandoned:
		assert.Equal(t, "op-1", claim.Op)
	case <-time.After(3 * time.Second):
		t.Fatal("the raced claim was not settled")
	}
	require.NoError(t, <-shutdownDone, "Shutdown waits for the settle and returns cleanly")

	res := awaitDeploy(t, resCh)
	require.ErrorIs(t, res.err, domain.ErrAppStateConflict, "a settled claim answers a conflict, never 202-running")
	require.NotNil(t, res.op)
	assert.Equal(t, int32(0), execCalls.Load(), "a claim settled during shutdown never executes")
}
