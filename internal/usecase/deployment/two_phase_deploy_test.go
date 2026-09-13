package deployment_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// TestStartDeploy_PersistsClaimBeforeAnyExecution proves the first phase
// durably claims the key with a non-terminal journal and touches no runtime
// resource: no image resolution, no pull, no workload mutation. A crash right
// after StartDeploy therefore leaves a claim reconciliation can converge.
func TestStartDeploy_PersistsClaimBeforeAnyExecution(t *testing.T) {
	t.Run("keyed", func(t *testing.T) {
		ctx := context.Background()
		state, runtime, images, secrets := mockDeps(t)
		rev := mockRevision()

		state.EXPECT().Recover(mock.Anything).Return(nil).Maybe()
		state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
		state.EXPECT().ClaimOperation(mock.Anything, mock.MatchedBy(func(op domain.AppOperation) bool {
			return op.Op == "k1" && !op.Terminal()
		})).RunAndReturn(func(_ context.Context, op domain.AppOperation) (domain.AppOperation, bool, error) {
			return op, true, nil
		}).Once()

		svc := keyedService(t, state, runtime, images, secrets)
		started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "k1"})

		require.NoError(t, err)
		require.True(t, started.Owned)
		require.Equal(t, "k1", started.Claim.Op)
		require.False(t, started.Claim.Journal.Terminal(), "the claim is non-terminal before execution")
		require.Len(t, started.Claim.Journal.Steps, 1)
		assert.Equal(t, domain.AppStepPending, started.Claim.Journal.Steps[0].State)

		state.AssertNotCalled(t, "LoadOwnership", mock.Anything, mock.Anything)
		images.AssertNotCalled(t, "ResolveDigest", mock.Anything, mock.Anything)
		runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
		runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
		runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	})

	t.Run("unkeyed", func(t *testing.T) {
		ctx := context.Background()
		state, runtime, images, secrets := mockDeps(t)
		rev := mockRevision()

		var saved []domain.AppOperation
		state.EXPECT().Recover(mock.Anything).Return(nil).Maybe()
		state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, op domain.AppOperation) error {
			saved = append(saved, op)
			return nil
		}).Maybe()
		state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

		svc := keyedService(t, state, runtime, images, secrets)
		started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog"})

		require.NoError(t, err)
		require.True(t, started.Owned)
		require.Len(t, saved, 1, "the claim journal is persisted before any effect")
		require.False(t, saved[0].Terminal())
		assert.Equal(t, domain.AppStepPending, saved[0].Steps[0].State)
		images.AssertNotCalled(t, "ResolveDigest", mock.Anything, mock.Anything)
		runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	})
}

// TestStartDeploy_SameKeyHandsOneOwner proves the second phase is only ever
// handed to the single claimer: a repeated key reports Owned false, carries
// the stored journal, and replays with an explicit conflict instead of a
// second execution.
func TestStartDeploy_SameKeyHandsOneOwner(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	stored := domain.AppOperation{
		Op: "k1", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Request: domain.AppOperationRequestFor("deploy", "blog", "", ""),
		Steps:   []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepPending}},
	}
	state.EXPECT().Recover(mock.Anything).Return(nil).Maybe()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	state.EXPECT().ClaimOperation(mock.Anything, mock.Anything).Return(domain.AppOperation{}, true, nil).Once()
	state.EXPECT().ClaimOperation(mock.Anything, mock.Anything).Return(stored, false, nil).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	first, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "k1"})
	require.NoError(t, err)
	require.True(t, first.Owned)

	second, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "k1"})
	require.NoError(t, err)
	require.False(t, second.Owned, "the key is never handed to a second owner")
	require.Equal(t, "k1", second.Claim.Journal.Op)
	require.ErrorIs(t, second.ReplayError(), domain.ErrAppStateConflict)

	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

// expectHeadlessDeploy wires one full sequential replacement of a headless
// service whose only effect is one container replacement, and records every
// journal write so the caller can assert claim-before-execution ordering.
func expectHeadlessDeploy(
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	images *outmocks.MockImageResolver,
	secrets *outmocks.MockSecretProvider,
) (domain.AppDesiredRevision, *[]domain.AppOperation) {
	svcSpec := webService()
	svcSpec.HTTP = nil
	svcSpec.Readiness = domain.AppReadiness{}
	rev := domain.AppDesiredRevision{
		Revision: "rev-1", App: "blog",
		Spec: domain.AppSpec{Name: "blog", Env: map[string]string{}, Services: []domain.AppService{svcSpec}},
	}

	saved := &[]domain.AppOperation{}
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, op domain.AppOperation) error {
		*saved = append(*saved, op)
		return nil
	}).Maybe()
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, svcSpec.Image).Return(restartTestDigest, nil)
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil)
	runtime.EXPECT().InspectImageVolumes(mock.Anything, svcSpec.Image).Return(nil, nil)
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	return rev, saved
}

// TestExecuteDeploy_RecordsTerminalOutcome proves the second phase executes
// the already claimed operation and records its terminal success, while the
// claim itself stayed non-terminal up to execution.
func TestExecuteDeploy_RecordsTerminalOutcome(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	_, saved := expectHeadlessDeploy(state, runtime, images, secrets)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.True(t, started.Owned)
	require.Len(t, *saved, 1)
	require.False(t, (*saved)[0].Terminal(), "the claim is non-terminal before execution")

	result, err := svc.ExecuteDeploy(ctx, started.Claim)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	last := (*saved)[len(*saved)-1]
	require.True(t, last.Terminal(), "the second phase records a terminal outcome")
	assert.Equal(t, domain.AppOutcomeSuccess, last.Outcome)
	runtime.AssertNumberOfCalls(t, "CreateContainer", 1)
}

// TestExecuteDeploy_RecordsTerminalFailure proves a preflight that fails
// during the second phase records the terminal failure in the claimed
// journal and runs no workload.
func TestExecuteDeploy_RecordsTerminalFailure(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	var saved []domain.AppOperation
	state.EXPECT().Recover(mock.Anything).Return(nil).Maybe()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, op domain.AppOperation) error {
		saved = append(saved, op)
		return nil
	}).Maybe()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return("", assert.AnError)

	svc := keyedService(t, state, runtime, images, secrets)
	started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.True(t, started.Owned)

	result, err := svc.ExecuteDeploy(ctx, started.Claim)

	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
	require.NotNil(t, result)
	require.NotEmpty(t, saved)
	last := saved[len(saved)-1]
	require.True(t, last.Terminal())
	assert.Equal(t, domain.AppOutcomeFailed, last.Outcome)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

// TestExecuteDeploy_TerminalStoreRecordBeatsStaleClaim proves the store is
// authoritative: a claim whose durable record was terminalized by another
// actor between the phases replays that outcome and never executes, even
// though the in-process claim still carries a non-terminal journal.
func TestExecuteDeploy_TerminalStoreRecordBeatsStaleClaim(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil).Maybe()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	state.EXPECT().ClaimOperation(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, op domain.AppOperation) (domain.AppOperation, bool, error) {
		return op, true, nil
	}).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "k1"})
	require.NoError(t, err)
	require.True(t, started.Owned)
	require.False(t, started.Claim.Journal.Terminal(), "the in-process claim is still non-terminal")

	// Another actor settled the key in the store: the stale in-memory copy
	// must not drive execution.
	expectStoredOperation(state, domain.AppOperation{
		Op: "k1", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Outcome: domain.AppOutcomeSuccess,
		Steps:   []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepSucceeded}},
	})

	result, err := svc.ExecuteDeploy(ctx, started.Claim)

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, domain.AppOutcomeSuccess, result.Outcome)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
}

// TestExecuteDeploy_CancellationLeavesRecoverableClaim proves cancellation or
// shutdown during the second phase leaves the durable claim non-terminal, and
// the existing reconciliation converges it on the next mutation instead of
// ever re-executing it.
func TestExecuteDeploy_CancellationLeavesRecoverableClaim(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	_, desired := seedReplaceableApp(t, ctx, store)
	runtime := outmocks.NewMockContainerRuntime(t)

	barrier := outmocks.NewMockGCBarrier(t)
	lease := outmocks.NewMockGCLease(t)
	barrier.EXPECT().AcquireShared(mock.Anything).Return(nil, context.Canceled).Once()
	barrier.EXPECT().AcquireShared(mock.Anything).Return(lease, nil).Once()
	lease.EXPECT().Release().Return().Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime,
		Images: outmocks.NewMockImageResolver(t), Secrets: outmocks.NewMockSecretProvider(t),
	}, zerowrap.Default()).WithGCBarrier(barrier)

	started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "op-cancel", Revision: desired.Revision})
	require.NoError(t, err)
	require.True(t, started.Owned)

	claimed, err := store.LoadOperation(ctx, "blog", "op-cancel")
	require.NoError(t, err)
	require.False(t, claimed.Terminal(), "the claim is durable and non-terminal before execution")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = svc.ExecuteDeploy(cancelled, started.Claim)
	require.ErrorIs(t, err, context.Canceled)

	interrupted, err := store.LoadOperation(ctx, "blog", "op-cancel")
	require.NoError(t, err)
	require.False(t, interrupted.Terminal(), "cancellation leaves the claim non-terminal for recovery")

	// The next mutation reconciles the interrupted claim before claiming its
	// own key: the operation is finalized and never executes twice.
	_, err = svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "op-next", Revision: desired.Revision})
	require.NoError(t, err)

	reconciled, err := store.LoadOperation(ctx, "blog", "op-cancel")
	require.NoError(t, err)
	require.True(t, reconciled.Terminal())
	assert.Equal(t, domain.AppOutcomeFailed, reconciled.Outcome)

	_, err = svc.ExecuteDeploy(ctx, deployment.DeployClaim{App: "blog", Op: "op-cancel"})
	require.ErrorIs(t, err, domain.ErrAppStateConflict, "a settled key never re-executes")
}

// TestDeploy_IsStartDeployThenExecuteDeploy proves the synchronous entry point
// is still the composition of both phases: it journals a non-terminal claim
// before any effect and a terminal outcome after the replacement.
func TestDeploy_IsStartDeployThenExecuteDeploy(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	_, saved := expectHeadlessDeploy(state, runtime, images, secrets)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	require.NotEmpty(t, *saved)
	require.False(t, (*saved)[0].Terminal(), "Deploy journals the claim before execution")
	require.True(t, (*saved)[len(*saved)-1].Terminal(), "Deploy records the terminal outcome")
}

// TestStartDeploy_LiveClaimSurvivesForegroundReconciliation pins the
// StartDeploy/ExecuteDeploy hand-off window: while an owner is between the
// claim and the coordinator (blocked before its GC lease), a same-key and a
// different-key foreground mutation must not reconcile, terminalize, or
// re-claim the live operation. The single owner then executes exactly the
// revision it claimed.
func TestStartDeploy_LiveClaimSurvivesForegroundReconciliation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	_, desired := seedReplaceableApp(t, ctx, store)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)

	// One full headless replacement, the only effects the live owner may run.
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/web:1.4.2").Return(restartTestDigest, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/web:1.4.2").Return(nil, nil).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	// Block ExecuteDeploy on its GC lease, before it takes the coordinator.
	entered := make(chan struct{})
	release := make(chan struct{})
	barrier := outmocks.NewMockGCBarrier(t)
	lease := outmocks.NewMockGCLease(t)
	barrier.EXPECT().AcquireShared(mock.Anything).Run(func(context.Context) {
		close(entered)
		<-release
	}).Return(lease, nil).Once()
	lease.EXPECT().Release().Return().Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithGCBarrier(barrier).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "op-live", Revision: desired.Revision})
	require.NoError(t, err)
	require.True(t, started.Owned, "the first claim owns the operation")

	done := make(chan error, 1)
	go func() {
		_, execErr := svc.ExecuteDeploy(ctx, started.Claim)
		done <- execErr
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for ExecuteDeploy to block before the coordinator")
	}

	// Same key while the owner is live: replay, never a second owner.
	same, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "op-live", Revision: desired.Revision})
	require.NoError(t, err)
	require.False(t, same.Owned, "a same-key replay never owns the operation twice")
	require.ErrorIs(t, same.ReplayError(), domain.ErrAppStateConflict, "the live claim was not terminalized")

	// Different key while the owner is live: the unfinished-operation guard
	// refuses, so no second claim is written.
	_, err = svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "op-other", Revision: "rev-0"})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)

	live, err := store.LoadOperation(ctx, "blog", "op-live")
	require.NoError(t, err)
	require.False(t, live.Terminal(), "a live operation is never terminalized by a foreground mutation")
	_, err = store.LoadOperation(ctx, "blog", "op-other")
	require.ErrorIs(t, err, domain.ErrAppOperationNotFound, "no second claim was written")

	close(release)
	require.NoError(t, <-done)

	final, err := store.LoadOperation(ctx, "blog", "op-live")
	require.NoError(t, err)
	require.True(t, final.Terminal())
	assert.Equal(t, domain.AppOutcomeSuccess, final.Outcome)
	assert.Equal(t, desired.Revision, final.InputRevision, "the owner executed the revision it claimed, not the stale one")
	runtime.AssertNumberOfCalls(t, "CreateContainer", 1)
}

// TestAbandonDeploy_SettlesClaimWithoutStarting proves a claimed but never
// scheduled operation can be settled safely: the journal becomes terminal, the
// in-process live marker is released, and a later claim succeeds instead of
// being refused as a permanently unfinished operation. No runtime effect is
// touched.
func TestAbandonDeploy_SettlesClaimWithoutStarting(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	_, desired := seedReplaceableApp(t, ctx, store)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())

	started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "op-abandon", Revision: desired.Revision})
	require.NoError(t, err)
	require.True(t, started.Owned)

	// While the claim is live, a different key is refused as unfinished.
	_, err = svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "op-blocked", Revision: desired.Revision})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)

	require.NoError(t, svc.AbandonDeploy(ctx, started.Claim))

	settled, err := store.LoadOperation(ctx, "blog", "op-abandon")
	require.NoError(t, err)
	require.True(t, settled.Terminal(), "a settled claim must not remain in flight")
	assert.Equal(t, domain.AppOutcomeFailed, settled.Outcome)

	// The live marker is gone and the journal terminal, so the next claim is
	// accepted cleanly instead of hanging on a false live operation.
	next, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Op: "op-next", Revision: desired.Revision})
	require.NoError(t, err)
	require.True(t, next.Owned, "a settled claim never blocks the next owner")
	require.NoError(t, svc.AbandonDeploy(ctx, next.Claim))

	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}
