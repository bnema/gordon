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

// recordSavedOperations captures every journal write so a test can assert the
// terminal preflight outcome and that no effect ran before the gate failed.
func recordSavedOperations(state *outmocks.MockAppState) *[]domain.AppOperation {
	saved := &[]domain.AppOperation{}
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, op domain.AppOperation) error {
			*saved = append(*saved, op)
			return nil
		})
	return saved
}

// assertNoDeployMutation proves a gate that failed before execution never
// touched the workload: no container is created, started, stopped, or
// removed, no volume is created, and ACTIVE is never republished.
func assertNoDeployMutation(t *testing.T, runtime *outmocks.MockContainerRuntime, state *outmocks.MockAppState) {
	t.Helper()
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateVolume", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
}

// assertPreflightGateJournal proves a failed preflight gate left a terminal
// failed journal whose preflight step carries the failure.
func assertPreflightGateJournal(t *testing.T, saved []domain.AppOperation) {
	t.Helper()
	require.NotEmpty(t, saved, "a failed gate must journal the claim and its failure")
	last := saved[len(saved)-1]
	require.True(t, last.Terminal(), "a failed preflight gate must persist a terminal journal")
	assert.Equal(t, domain.AppOutcomeFailed, last.Outcome)
	step, ok := opStep(last, "preflight")
	require.True(t, ok)
	assert.Equal(t, domain.AppStepFailed, step.State)
}

// TestDeploy_MissingRequiredSecretFailsClosed proves a deploy whose required
// secret cannot be resolved fails closed with ErrAppSecretMissing before any
// workload mutation, and journals the terminal failure.
func TestDeploy_MissingRequiredSecretFailsClosed(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return(restartTestDigest, nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("", assert.AnError).Once()
	saved := recordSavedOperations(state)

	svc := keyedService(t, state, runtime, images, secrets)
	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})

	require.ErrorIs(t, err, domain.ErrAppSecretMissing)
	assertNoDeployMutation(t, runtime, state)
	assertPreflightGateJournal(t, *saved)
}

// TestDeploy_EnvSecretCollisionFailsClosed proves a stored revision whose
// public env collides with one of the service's secret keys is refused
// before any image resolution or workload mutation.
func TestDeploy_EnvSecretCollisionFailsClosed(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()
	rev.Spec.Env = map[string]string{"DATABASE_URL": "public-value"}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	saved := recordSavedOperations(state)

	svc := keyedService(t, state, runtime, images, secrets)
	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})

	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	images.AssertNotCalled(t, "ResolveDigest", mock.Anything, mock.Anything)
	secrets.AssertNotCalled(t, "GetSecret", mock.Anything, mock.Anything)
	assertNoDeployMutation(t, runtime, state)
	assertPreflightGateJournal(t, *saved)
}

// TestDeploy_UnmanagedImageVolumeRejected proves an image that declares a
// VOLUME with no operator mapping is refused before any workload mutation,
// and journals the terminal failure.
func TestDeploy_UnmanagedImageVolumeRejected(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return(restartTestDigest, nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, rev.Spec.Services[0].Image).Return([]string{"/data"}, nil).Once()
	saved := recordSavedOperations(state)

	svc := keyedService(t, state, runtime, images, secrets)
	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})

	require.ErrorIs(t, err, domain.ErrAppUnmanagedImageVolume)
	assertNoDeployMutation(t, runtime, state)
	assertPreflightGateJournal(t, *saved)
}

// TestDeploy_RefusesUnownedExistingVolume proves R2: a generated runtime
// volume that already exists but is NOT recorded in the app's ownership is
// refused fail-closed — the deploy must never implicitly adopt (and modify)
// foreign data under a reused name.
func TestDeploy_RefusesUnownedExistingVolume(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	svcSpec := webService()
	svcSpec.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}}
	rev := testRevision("blog", svcSpec)
	runtimeName := domain.RuntimeVolumeName("blog", "web", "data")

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	// Ownership records no volume, so the runtime one is foreign.
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	images.EXPECT().ResolveDigest(mock.Anything, svcSpec.Image).Return(restartTestDigest, nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, svcSpec.Image).Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	runtime.EXPECT().VolumeExists(mock.Anything, runtimeName).Return(true, nil).Once()
	saved := recordSavedOperations(state)

	svc := keyedService(t, state, runtime, images, secrets)
	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})

	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.Contains(t, err.Error(), "not owned by app")
	assertNoDeployMutation(t, runtime, state)
	assertPreflightGateJournal(t, *saved)
}

// TestStartDeploy_TargetedDiffPolicy proves the targeted-deploy convergence
// rules live in the claim phase: a requested-service image change is allowed
// and claims the operation, while app-wide or other-service divergence is
// refused with ErrAppStateConflict before any claim is written and without
// resolving an image or touching the runtime.
func TestStartDeploy_TargetedDiffPolicy(t *testing.T) {
	baseService := webService()
	worker := webService()
	worker.Name = "worker"
	worker.HTTP = []domain.AppHTTPInterface{{Host: "worker.example.com", Port: 8080, TLS: domain.AppTLSAuto}}
	base := domain.AppActive{
		App: "blog", ConvergedRevision: "rev-1", Converged: true,
		Networks: []domain.AppSharedNetwork{{Network: "shared", Services: []string{"web"}}},
		Services: map[string]domain.AppEffectiveService{
			"web":    {Spec: baseService},
			"worker": {Spec: worker},
		},
	}

	tests := []struct {
		name    string
		mutate  func(*domain.AppSpec)
		wantErr bool
	}{
		{name: "requested service image", mutate: func(spec *domain.AppSpec) { spec.Services[0].Image = "img:2" }},
		{name: "other service", wantErr: true, mutate: func(spec *domain.AppSpec) { spec.Services[1].Image = "img:2" }},
		{name: "environment", wantErr: true, mutate: func(spec *domain.AppSpec) { spec.Env = map[string]string{"MODE": "prod"} }},
		{name: "network", wantErr: true, mutate: func(spec *domain.AppSpec) { spec.Networks[0].Aliases = []string{"peer"} }},
		{name: "service added", wantErr: true, mutate: func(spec *domain.AppSpec) {
			spec.Services = append(spec.Services, domain.AppService{Name: "job", Image: "img:1", Readiness: domain.AppReadiness{Type: "none", Timeout: 30 * time.Second}})
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state, runtime, images, secrets := mockDeps(t)
			spec := domain.AppSpec{
				Name:     "blog",
				Networks: append([]domain.AppSharedNetwork(nil), base.Networks...),
				Services: []domain.AppService{baseService, worker},
			}
			spec.Networks[0].Aliases = append([]string(nil), base.Networks[0].Aliases...)
			tc.mutate(&spec)
			rev := domain.AppDesiredRevision{App: "blog", Revision: "rev-2", Spec: spec}

			state.EXPECT().Recover(mock.Anything).Return(nil)
			state.EXPECT().LoadRevision(mock.Anything, "blog", "rev-2").Return(rev, nil).Once()
			state.EXPECT().LoadActive(mock.Anything, "blog").Return(base, true, nil).Once()
			if !tc.wantErr {
				state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil).Once()
			}

			svc := keyedService(t, state, runtime, images, secrets)
			started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog", Revision: "rev-2", Service: "web"})

			if tc.wantErr {
				require.ErrorIs(t, err, domain.ErrAppStateConflict)
				state.AssertNotCalled(t, "SaveOperation", mock.Anything, mock.Anything)
			} else {
				require.NoError(t, err)
				require.True(t, started.Owned)
			}
			images.AssertNotCalled(t, "ResolveDigest", mock.Anything, mock.Anything)
			assertNoDeployMutation(t, runtime, state)
		})
	}
}

// TestDeploy_HoldsSharedGCBarrierAcrossResourceSelection proves the shared
// GC lease is taken by the execution phase before resource selection and
// released only when the mutation ends. Prune can therefore never observe a
// resource that was selected but whose protection is not yet durable.
func TestDeploy_HoldsSharedGCBarrierAcrossResourceSelection(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	svcSpec := webService()
	svcSpec.HTTP = nil
	svcSpec.Readiness = domain.AppReadiness{}
	rev := testRevision("blog", svcSpec)

	barrier := outmocks.NewMockGCBarrier(t)
	lease := outmocks.NewMockGCLease(t)
	held := false
	barrier.EXPECT().AcquireShared(mock.Anything).Run(func(context.Context) {
		held = true
	}).Return(lease, nil).Once()
	lease.EXPECT().Release().Run(func() {
		held = false
	}).Once()

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	// Image resolution and image-volume inspection are the resource-selection
	// steps: they must happen inside the shared lease.
	images.EXPECT().ResolveDigest(mock.Anything, svcSpec.Image).Run(func(context.Context, string) {
		require.True(t, held, "image resolution must run inside the shared GC lease")
	}).Return(restartTestDigest, nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil)
	runtime.EXPECT().InspectImageVolumes(mock.Anything, svcSpec.Image).Run(func(context.Context, string) {
		require.True(t, held, "image-volume inspection must run inside the shared GC lease")
	}).Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	)).WithGCBarrier(barrier)

	started, err := svc.StartDeploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.True(t, started.Owned)
	require.False(t, held, "the claim phase takes no shared GC lease")

	result, err := svc.ExecuteDeploy(ctx, started.Claim)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.False(t, held, "the shared lease must be released when the operation ends")
}
