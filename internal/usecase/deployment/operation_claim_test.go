package deployment_test

import (
	"context"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

func keyedService(
	t *testing.T,
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	images *outmocks.MockImageResolver,
	secrets *outmocks.MockSecretProvider,
) *deployment.Service {
	t.Helper()
	return deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
}

// TestDeploy_ClaimPersistsRequestIdentity proves the first claim writes the
// journal with the immutable request identity of the caller's inputs, so a
// reused key for another request can be rejected later.
func TestDeploy_ClaimPersistsRequestIdentity(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().ClaimOperation(mock.Anything, mock.MatchedBy(func(op domain.AppOperation) bool {
		return op.Op == "key-1" &&
			op.Kind == "deploy" &&
			op.Request == domain.AppOperationRequestFor("deploy", "blog", "", "") &&
			!op.Terminal()
	})).RunAndReturn(func(_ context.Context, op domain.AppOperation) (domain.AppOperation, bool, error) {
		return op, true, nil
	}).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil).Once()
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return("", assert.AnError).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "key-1"})

	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
	require.NotNil(t, result)
	assert.Equal(t, "key-1", result.Op)
}

// TestDeploy_TerminalKeyReplayExecutesNoEffect proves a repeat of a
// finished request returns its stored journal without touching a workload.
func TestDeploy_TerminalKeyReplayExecutesNoEffect(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	stored := domain.AppOperation{
		Op: "key-1", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Request: domain.AppOperationRequestFor("deploy", "blog", "", ""),
		Outcome: domain.AppOutcomeSuccess,
		Steps:   []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepSucceeded}},
	}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().ClaimOperation(mock.Anything, mock.Anything).Return(stored, false, nil).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "key-1"})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "key-1", result.Op)
	assert.Equal(t, domain.AppOutcomeSuccess, result.Outcome)
	state.AssertNotCalled(t, "SaveOperation", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

// TestDeploy_PendingKeyReplayConflicts proves an in-flight (or interrupted)
// claim is never re-executed: the caller receives the journal and a
// conflict instead of a second deployment.
func TestDeploy_PendingKeyReplayConflicts(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	pending := domain.AppOperation{
		Op: "key-1", Kind: "deploy", App: "blog", InputRevision: "rev-1",
		Request: domain.AppOperationRequestFor("deploy", "blog", "", ""),
		Steps:   []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepPending}},
	}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().ClaimOperation(mock.Anything, mock.Anything).Return(pending, false, nil).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "key-1"})

	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.NotNil(t, result, "the interrupted journal is returned for inspection")
	assert.Empty(t, result.Outcome)
	state.AssertNotCalled(t, "SaveOperation", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

// TestDeploy_MismatchedKeyIsRejected proves one key cannot answer a
// different request.
func TestDeploy_MismatchedKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().ClaimOperation(mock.Anything, mock.Anything).
		Return(domain.AppOperation{}, false, domain.ErrAppStateConflict).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "key-1"})

	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	assert.Nil(t, result)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

// TestDeploy_UnknownAppIsNotFound proves deploying a name with no app
// identity reports the missing app and claims nothing.
func TestDeploy_UnknownAppIsNotFound(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "ghost").Return(domain.AppDesiredRevision{}, false, nil).Once()
	state.EXPECT().AppExists(mock.Anything, "ghost").Return(false, nil).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "ghost", Op: "key-1"})

	require.ErrorIs(t, err, domain.ErrAppNotFound)
	assert.Nil(t, result)
	state.AssertNotCalled(t, "ClaimOperation", mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "SaveOperation", mock.Anything, mock.Anything)
}

// TestDeploy_UnknownExplicitRevisionOnKnownApp proves a missing explicit
// revision of a known app stays a revision error, not an app error.
func TestDeploy_UnknownExplicitRevisionOnKnownApp(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadRevision(mock.Anything, "blog", "rev-404").
		Return(domain.AppDesiredRevision{}, domain.ErrAppRevisionNotFound).Once()
	state.EXPECT().AppExists(mock.Anything, "blog").Return(true, nil).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Revision: "rev-404", Op: "key-1"})

	require.ErrorIs(t, err, domain.ErrAppRevisionNotFound)
	state.AssertNotCalled(t, "ClaimOperation", mock.Anything, mock.Anything)
}

// TestRemove_TerminalKeyReplaySkipsRuntime proves a repeated keyed removal
// replays its stored result without touching the runtime again.
func TestRemove_TerminalKeyReplaySkipsRuntime(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	stored := domain.AppOperation{
		Op: "key-1", Kind: "remove", App: "blog",
		Request: domain.AppOperationRequestFor("remove", "blog", "", ""),
		Outcome: domain.AppOutcomeSuccess,
	}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().ClaimOperation(mock.Anything, mock.Anything).Return(stored, false, nil).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	result, err := svc.Remove(ctx, "blog", "key-1")

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "key-1", result.Op)
	assert.Equal(t, domain.AppOutcomeSuccess, result.Outcome)
	state.AssertNotCalled(t, "SaveIntent", mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "RetireApp", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything)
}

// TestStop_PendingKeyReplayConflicts proves a stop replay of an unfinished
// operation never re-stops a workload.
func TestStop_PendingKeyReplayConflicts(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	pending := domain.AppOperation{
		Op: "key-1", Kind: "stop", App: "blog",
		Request: domain.AppOperationRequestFor("stop", "blog", "", ""),
		Steps:   []domain.AppOperationStep{{ID: "intent.stopped", State: domain.AppStepPending}},
	}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().ClaimOperation(mock.Anything, mock.Anything).Return(pending, false, nil).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	result, err := svc.Stop(ctx, "blog", "key-1")

	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.NotNil(t, result)
	state.AssertNotCalled(t, "SaveIntent", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything)
}

// TestRestart_ClaimCarriesServiceIdentity proves the restart claim records
// the targeted service, so a key reused for another service conflicts.
func TestRestart_ClaimCarriesServiceIdentity(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().ClaimOperation(mock.Anything, mock.MatchedBy(func(op domain.AppOperation) bool {
		return op.Request == domain.AppOperationRequestFor("restart", "blog", "", "web")
	})).Return(domain.AppOperation{}, false, domain.ErrAppStateConflict).Once()

	svc := keyedService(t, state, runtime, images, secrets)
	_, err := svc.Restart(ctx, "blog", "web", "key-1")

	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
}
