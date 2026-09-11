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

func testRevision(app string, services ...domain.AppService) domain.AppDesiredRevision {
	for i := range services {
		if services[i].StopGrace == 0 {
			services[i].StopGrace = 10 * time.Second
		}
		if services[i].Readiness.Timeout == 0 {
			services[i].Readiness.Timeout = 30 * time.Second
		}
	}
	return domain.AppDesiredRevision{
		Revision: "rev-1", App: app,
		Spec: domain.AppSpec{Name: app, Env: map[string]string{}, Services: services},
	}
}

func webService() domain.AppService {
	return domain.AppService{
		Name: "web", Image: "registry.example.com/blog/web:1.4.2",
		Readiness: domain.AppReadiness{Type: "http", Path: "/healthz"},
		HTTP:      []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
		Secrets:   map[string]string{"DATABASE_URL": "database-url"},
	}
}

func preflightService(
	t *testing.T,
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	images *outmocks.MockImageResolver,
	secrets *outmocks.MockSecretProvider,
) *deployment.Service {
	t.Helper()
	return deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		ImagePolicy: domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))
}

// expectFullPreflight wires every preflight gate on mocks.
func expectFullPreflight(
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	images *outmocks.MockImageResolver,
	secrets *outmocks.MockSecretProvider,
	rev domain.AppDesiredRevision,
	imageVolumes []string,
	digest string,
	secretValue string,
	secretErr error,
) {
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, rev.App).Return(rev, true, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, rev.App).Return(domain.AppOwnership{App: rev.App}, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return(digest, nil).Once()
	if secretErr != nil {
		secrets.EXPECT().GetSecret(mock.Anything, mock.Anything).Return("", secretErr).Once()
		return
	}
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/blog/web/database-url").Return(secretValue, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, rev.Spec.Services[0].Image).Return(imageVolumes, nil).Once()
	if secretErr == nil && imageVolumes == nil {
		state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	}
}

func TestPreflight_PassesAndPins(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	rev := testRevision("blog", webService())
	expectFullPreflight(state, runtime, images, secrets, rev, nil, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "s3cr3t", nil)
	svc := preflightService(t, state, runtime, images, secrets)

	pinned, op, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, pinned)
	require.NotNil(t, op)
	assert.Equal(t, "rev-1", op.InputRevision)
	require.Len(t, op.Steps, 2)
	assert.Equal(t, "succeeded", op.Steps[0].State)
	assert.Contains(t, op.Steps[1].Digest, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	assert.Equal(t, "web", op.Steps[1].Service)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestPreflight_ImageFailureActivatesNothing(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	rev := testRevision("blog", webService())

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog"}, nil)
	images.EXPECT().ResolveDigest(mock.Anything, mock.Anything).Return("", assert.AnError).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	svc := preflightService(t, state, runtime, images, secrets)

	_, op, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
	require.NotNil(t, op)
	assert.Equal(t, "failed", op.Outcome)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestPreflight_SecretMissingFailsClosed(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	rev := testRevision("blog", webService())
	expectFullPreflight(state, runtime, images, secrets, rev, nil, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", assert.AnError)
	svc := preflightService(t, state, runtime, images, secrets)

	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppSecretMissing)
}

func TestPreflight_UnmanagedImageVolumeRejected(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	rev := testRevision("blog", webService())
	expectFullPreflight(state, runtime, images, secrets, rev, []string{"/data"}, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "x", nil)
	svc := preflightService(t, state, runtime, images, secrets)

	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppUnmanagedImageVolume)
}

// TestPreflight_RefusesUnownedExistingVolume proves R2: a generated
// runtime volume that already exists but is NOT recorded in the app's
// ownership is refused fail-closed — the deploy must never implicitly
// adopt (and modify) foreign data under a reused name.
func TestPreflight_RefusesUnownedExistingVolume(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	svc := webService()
	svc.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}}
	rev := testRevision("blog", svc)
	expectFullPreflight(state, runtime, images, secrets, rev, nil, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "x", nil)
	// Ownership knows no volumes; the runtime one is foreign.
	runtime.EXPECT().VolumeExists(mock.Anything, "gordon-blog--web--vol--data").Return(true, nil).Once()
	svcSvc := preflightService(t, state, runtime, images, secrets)

	_, _, err := svcSvc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.Contains(t, err.Error(), "not owned by app")
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

// TestPreflight_AcceptsOwnedExistingVolume proves R2 does not break the
// legitimate reuse path: a volume created by this app's own earlier
// deploy (recorded in ownership) is accepted.
func TestPreflight_AcceptsOwnedExistingVolume(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	svc := webService()
	svc.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}}
	rev := testRevision("blog", svc)
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{
		App: "blog",
		Volumes: []domain.AppOwnedVolume{{
			Name: "data", Service: "web",
			RuntimeName: "gordon-blog--web--vol--data",
			State:       domain.AppResourceAttached,
		}},
	}, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	images.EXPECT().ResolveDigest(mock.Anything, mock.Anything).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/blog/web/database-url").Return("x", nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, mock.Anything).Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	runtime.EXPECT().VolumeExists(mock.Anything, "gordon-blog--web--vol--data").Return(true, nil).Once()
	svcSvc := preflightService(t, state, runtime, images, secrets)

	pinned, _, err := svcSvc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.Len(t, pinned, 1)
}

func TestPreflight_TargetedRefusesDivergence(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	rev := testRevision("blog", webService())
	rev.Revision = "rev-2"

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadRevision(mock.Anything, "blog", "rev-2").Return(rev, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{App: "blog", ConvergedRevision: "rev-1", Converged: true}, true, nil).Once()
	svc := preflightService(t, state, runtime, images, secrets)

	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog", Revision: "rev-2", Service: "web"})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
}

func TestPreflight_TargetedConvergedPasses(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	rev := testRevision("blog", webService())

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{App: "blog", ConvergedRevision: "rev-1", Converged: true}, true, nil).Once()
	images.EXPECT().ResolveDigest(mock.Anything, mock.Anything).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, mock.Anything).Return("x", nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, mock.Anything).Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog"}, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil).Times(2)
	svc := preflightService(t, state, runtime, images, secrets)

	_, op, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog", Service: "web"})
	require.NoError(t, err)
	require.NotNil(t, op)
}

func TestPreflight_MutableTagReresolved(t *testing.T) {
	ctx := context.Background()
	newDeps := func() (*outmocks.MockAppState, *outmocks.MockContainerRuntime, *outmocks.MockImageResolver, *outmocks.MockSecretProvider) {
		return outmocks.NewMockAppState(t), outmocks.NewMockContainerRuntime(t), outmocks.NewMockImageResolver(t), outmocks.NewMockSecretProvider(t)
	}
	svcSpec := webService()
	svcSpec.Image = "registry.example.com/blog/web:latest"
	rev := testRevision("blog", svcSpec)

	state, runtime, images, secrets := newDeps()
	expectFullPreflight(state, runtime, images, secrets, rev, nil, "sha256:first", "x", nil)
	svc := preflightService(t, state, runtime, images, secrets)
	_, op1, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	assert.Contains(t, op1.Steps[1].Digest, "sha256:first")

	state2, runtime2, images2, secrets2 := newDeps()
	expectFullPreflight(state2, runtime2, images2, secrets2, rev, nil, "sha256:second", "x", nil)
	svc2 := preflightService(t, state2, runtime2, images2, secrets2)
	_, op2, err := svc2.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	assert.Contains(t, op2.Steps[1].Digest, "sha256:second")
	assert.NotEqual(t, op1.Op, op2.Op)
}

// TestPreflight_RejectsCrossAppReservationConflict proves preflight
// re-validates the candidate against the live global table with the
// same wildcard-aware policy as apply.
func TestPreflight_RejectsCrossAppReservationConflict(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	svcSpec := webService()
	svcSpec.HTTP = nil
	svcSpec.TCP = []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "127.0.0.1:19090"}}
	rev := testRevision("blog", svcSpec)
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog"}, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	images.EXPECT().ResolveDigest(mock.Anything, svcSpec.Image).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/blog/web/database-url").Return("x", nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, svcSpec.Image).Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{
		Reservations: []domain.AppListenerReservation{{Proto: "tcp", IP: "dual", Port: 19090, Service: "s", App: "other"}},
	}, nil).Once()
	svc := preflightService(t, state, runtime, images, secrets)

	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestComputeOutcome_TerminalResults(t *testing.T) {
	deployed := deployment.ServiceResult{Result: "deployed"}
	failed := deployment.ServiceResult{Result: "failed", Error: "boom"}
	assert.Equal(t, "success", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": deployed}))
	assert.Equal(t, "failed", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": failed}))
	assert.Equal(t, "partial", deployment.ComputeOutcome(map[string]deployment.ServiceResult{"a": deployed, "b": failed}))
	assert.Equal(t, "failed", deployment.ComputeOutcome(map[string]deployment.ServiceResult{}))
}

// TestReconcileBoot_ContinuesAfterAppFailure proves R1: a failing app
// never blocks verification of the following apps, and failures are
// aggregated instead of aborting at the first one.
func TestReconcileBoot_ContinuesAfterAppFailure(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	svcSvc := preflightService(t, state, runtime, images, secrets)

	badEff := domain.AppEffectiveService{
		Container:    "ctr-bad",
		Spec:         webService(),
		BackendBinds: map[int]int{8080: 32768},
	}
	badActive := domain.AppActive{App: "bad", Services: map[string]domain.AppEffectiveService{"web": badEff}}
	goodEff := domain.AppEffectiveService{
		Container: "ctr-good",
		Spec:      webService(),
	}
	goodActive := domain.AppActive{App: "good", Services: map[string]domain.AppEffectiveService{"web": goodEff}}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"bad", "good"}, nil).Once()
	// bad app: Start fails bind verification, binds withdrawn.
	state.EXPECT().LoadIntent(mock.Anything, "bad").Return(domain.AppStopIntent{App: "bad"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "bad").Return(badActive, true, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "bad").Return(nil, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "ctr-bad").Return(true, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "ctr-bad", mock.Anything).Return(nil, assert.AnError).Once()
	// No traffic boundary is wired here, so the fail-closed bind
	// withdrawal is a no-op and deployment writes no ACTIVE binds itself.
	// good app: still verified after the bad one failed.
	state.EXPECT().LoadIntent(mock.Anything, "good").Return(domain.AppStopIntent{App: "good"}, nil).Once()
	// Start load + bind-refresh reload.
	state.EXPECT().LoadActive(mock.Anything, "good").Return(goodActive, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "good").Return(nil, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "ctr-good").Return(true, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "ctr-good", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32777, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	// Bind refresh persists the new bind (differs from recorded nil).
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()

	err := svcSvc.ReconcileBoot(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"bad"`)
}

func TestPreflight_EnvCollisionFailsClosed(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppState(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	svc := webService()
	rev := testRevision("blog", svc)
	rev.Spec.Env = map[string]string{"DATABASE_URL": "public-value"}

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog"}, nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	svcSvc := preflightService(t, state, runtime, images, secrets)

	_, _, err := svcSvc.Preflight(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrInvalidAppSpec)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}
