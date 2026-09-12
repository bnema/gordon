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

// TestDeploy_RetirementFailureIsSurfacedAsAWarning proves a leftover of a
// successful deploy is journaled as a bounded warning instead of being
// dropped, and that the deploy itself still reports success.
func TestDeploy_RetirementFailureIsSurfacedAsAWarning(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()
	rev.Spec.Services[0].StopGrace = time.Millisecond

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
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18080, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()
	// The replaced container is retired with its own effective grace, and
	// its removal fails: the deploy is still successful, the leftover is
	// a warning.
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
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	require.Len(t, result.CleanupWarnings, 1)
	assert.Equal(t, "c-old", result.CleanupWarnings[0].Leftover)
	assert.NotContains(t, result.CleanupWarnings[0].Detail, "unexpected", "warnings carry stable, log-free detail")

	require.NotEmpty(t, saved)
	last := saved[len(saved)-1]
	require.Len(t, last.Steps, 2)
	assert.Equal(t, "preflight", last.Steps[0].ID)
	assert.Equal(t, domain.AppStepSucceeded, last.Steps[0].State)
	assert.Equal(t, "service.web.replace", last.Steps[1].ID)
	assert.Equal(t, domain.AppStepSucceeded, last.Steps[1].State)
	require.Len(t, last.Warnings, 1)
	assert.Equal(t, "web", last.Warnings[0].Service)
	assert.Equal(t, "c-old", last.Warnings[0].Leftover)
}
