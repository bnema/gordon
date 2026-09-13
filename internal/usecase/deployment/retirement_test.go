package deployment_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// graceSpec is a headless service carrying one explicit stop grace.
func graceSpec(grace time.Duration) domain.AppService {
	spec := headlessSpec()
	spec.StopGrace = grace
	return spec
}

func activeWithGrace(container string, grace time.Duration) domain.AppActive {
	return domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: container, EffectiveRevision: "rev-1", Spec: graceSpec(grace)},
	}}
}

// TestLifecycleVerbsPassTheEffectiveStopGrace proves every runtime stop
// uses the effective service grace instead of a fixed adapter timeout.
func TestLifecycleVerbsPassTheEffectiveStopGrace(t *testing.T) {
	const grace = 25 * time.Second

	t.Run("stop", func(t *testing.T) {
		ctx := context.Background()
		state, runtime, _, _ := mockDeps(t)
		active := activeWithGrace("c-1", grace)
		state.EXPECT().Recover(mock.Anything).Return(nil)
		state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
		state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil).Once()
		runtime.EXPECT().StopContainer(mock.Anything, "c-1", grace).Return(nil).Once()
		runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(nil).Once()
		state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()
		state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-1").Return(nil).Once()
		state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

		svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime}, zerowrap.Default())
		_, err := svc.Stop(ctx, "blog", "")
		require.NoError(t, err)
	})

	t.Run("restart", func(t *testing.T) {
		ctx := context.Background()
		state, runtime, _, _ := mockDeps(t)
		active := activeWithGrace("c-1", grace)
		state.EXPECT().Recover(mock.Anything).Return(nil)
		state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
		state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
		runtime.EXPECT().RestartContainer(mock.Anything, "c-1", grace).Return(nil).Once()
		state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

		svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime}, zerowrap.Default())
		_, err := svc.Restart(ctx, "blog", "web", "")
		require.NoError(t, err)
	})

	t.Run("remove", func(t *testing.T) {
		ctx := context.Background()
		state, runtime, _, _ := mockDeps(t)
		active := activeWithGrace("c-1", grace)
		state.EXPECT().Recover(mock.Anything).Return(nil)
		state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
		state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil).Once()
		state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).Return(nil).Once()
		runtime.EXPECT().StopContainer(mock.Anything, "c-1", grace).Return(nil).Once()
		runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(nil).Once()
		state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()
		state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-1").Return(nil).Once()
		state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog"}, nil).Once()
		state.EXPECT().RetireApp(mock.Anything, "blog").Return(nil).Once()
		state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

		svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime}, zerowrap.Default())
		_, err := svc.Remove(ctx, "blog", "")
		require.NoError(t, err)
	})
}

// TestStopWithoutDeclaredGraceUsesTheAppDefault proves an effective spec
// with no stop_grace still reaches the runtime as the app default instead
// of an immediate kill.
func TestStopWithoutDeclaredGraceUsesTheAppDefault(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	active := activeWithGrace("c-1", 0)
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", domain.AppDefaultStopGrace).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-1").Return(nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime}, zerowrap.Default())
	_, err := svc.Stop(ctx, "blog", "")
	require.NoError(t, err)
}

// TestRemove_FailedRetirementKeepsClaimsAndActiveState proves a container
// that is not confirmed gone keeps its backend claims, keeps its ACTIVE
// reference, and fails the operation terminally.
func TestRemove_FailedRetirementKeepsClaimsAndActiveState(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	active := activeWithGrace("c-1", time.Second)
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", time.Second).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(assert.AnError).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime}, zerowrap.Default())
	_, err := svc.Remove(ctx, "blog", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not confirmed gone")
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ClearRecoveryInhibition", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "RetireApp", mock.Anything, mock.Anything)
}

// TestDeploy_RetirementFailureBlocksReplacement proves the superseded
// generation must be confirmed gone before a replacement starts: a failed
// retirement aborts the service without creating a candidate, and the
// failure is journaled instead of being downgraded to a warning.
func TestDeploy_RetirementFailureBlocksReplacement(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	oldActive := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old", Spec: graceSpec(time.Second)},
	}}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return(
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil)
	runtime.EXPECT().InspectImageVolumes(mock.Anything, rev.Spec.Services[0].Image).Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(oldActive, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	// The superseded container is retired with its own effective grace, and
	// its removal fails: the replacement must not start.
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", time.Second).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(assert.AnError).Once()

	var saved []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, op domain.AppOperation) error {
			saved = append(saved, op)
			return nil
		})

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Contains(t, result.Services["web"].Error, "retire superseded container",
		"the unconfirmed retirement is the reported failure")

	// No workload is created, started, or published while the superseded
	// generation is not confirmed gone, and no claim or inhibition of that
	// generation is released.
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "ListNetworks", mock.Anything)
	runtime.AssertNotCalled(t, "RemoveVolume", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ClearRecoveryInhibition", mock.Anything, mock.Anything, mock.Anything, mock.Anything)

	require.NotEmpty(t, saved)
	last := saved[len(saved)-1]
	require.Len(t, last.Steps, 2)
	assert.Equal(t, "preflight", last.Steps[0].ID)
	assert.Equal(t, domain.AppStepSucceeded, last.Steps[0].State)
	assert.Equal(t, "service.web.replace", last.Steps[1].ID)
	assert.Equal(t, domain.AppStepFailed, last.Steps[1].State)
	assert.Equal(t, domain.AppOutcomeFailed, last.Outcome)
}
