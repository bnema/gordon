package deployment_test

import (
	"context"
	"errors"
	"io"
	"strings"
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

func mockDeps(t *testing.T) (*outmocks.MockAppState, *outmocks.MockContainerRuntime, *outmocks.MockImageResolver, *outmocks.MockSecretProvider) {
	t.Helper()
	return outmocks.NewMockAppState(t),
		outmocks.NewMockContainerRuntime(t),
		outmocks.NewMockImageResolver(t),
		outmocks.NewMockSecretProvider(t)
}

func mockRevision() domain.AppDesiredRevision {
	svc := webService()
	svc.Image = "docker.io/example/web:1.4.2"
	svc.Readiness.Timeout = time.Second
	return domain.AppDesiredRevision{
		Revision: "rev-1", App: "blog",
		Spec: domain.AppSpec{Name: "blog", Env: map[string]string{}, Services: []domain.AppService{svc}},
	}
}

// expectDeployPreflight wires recover + revision + digest + secrets +
// image volumes + checkpoint + ownership + the two journal writes that
// frame execution (preflight table, then per-service/outcome updates).
func expectDeployPreflight(
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	images *outmocks.MockImageResolver,
	secrets *outmocks.MockSecretProvider,
	rev domain.AppDesiredRevision,
) {
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil)
	runtime.EXPECT().InspectImageVolumes(mock.Anything, rev.Spec.Services[0].Image).Return(nil, nil)
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
}

// expectNetworkProvision registers the two runtime calls every
// create/recovery path makes when the incarnation network does not yet
// exist: inspect existing networks (none), then create the private one.
func expectNetworkProvision(runtime *outmocks.MockContainerRuntime, appID string, times int) {
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{}, nil).Times(times)
	runtime.EXPECT().CreateNetwork(mock.Anything, domain.AppPrivateNetworkName("gordon", appID), mock.Anything).Return(nil).Times(times)
}

func TestDeploy_PreflightFailureReturnsJournaledOperation(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil).Twice()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil).Once()
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return("", assert.AnError).Once()

	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets}, zerowrap.Default())
	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})

	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
	require.NotNil(t, result)
	assert.NotEmpty(t, result.Op)
	assert.Equal(t, "blog", result.App)
	assert.Equal(t, rev.Revision, result.Revision)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestDeploy_Mockery_PullsPinnedImageWithRegistryAuth(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	svcSpec := webService()
	svcSpec.Image = "registry.example.com/blog/web:1.4.2"
	svcSpec.HTTP = nil
	svcSpec.Readiness = domain.AppReadiness{}
	rev := domain.AppDesiredRevision{
		Revision: "rev-1", App: "blog",
		Spec: domain.AppSpec{Name: "blog", Env: map[string]string{}, Services: []domain.AppService{svcSpec}},
	}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, svcSpec.Image).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil)
	runtime.EXPECT().PullImageWithAuth(mock.Anything, "127.0.0.1:5000/blog/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "gordon", "s3cret").Return(nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "127.0.0.1:5000/blog/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa").Return(nil, nil)
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App:      "blog",
		Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	expectNetworkProvision(runtime, "app-blog", 1)

	// The pinned ref was pulled during preflight before image-volume inspection.
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.Image == "127.0.0.1:5000/blog/web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" &&
			assert.Equal(t, svcSpec.Command, cfg.Entrypoint) && assert.Empty(t, cfg.Cmd)
	})).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	// No TCP-capable interfaces (HTTP nil, readiness empty): no backend
	// binds published, no GetContainerBackendBinds call.
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App:      "blog",
		Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(
		deployment.Deps{
			State: state, Runtime: runtime, Images: images, Secrets: secrets,
			Registry: deployment.RegistryConfig{
				Domain:      "registry.example.com",
				PullAddress: "127.0.0.1:5000",
				Username:    "gordon",
				Password:    "s3cret",
			},
		},
		zerowrap.Default(),
	).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Empty(t, result.Services["web"].BackendBinds)
}

// TestDeploy_Mockery_HTTPSuccessRecordsLoopbackBinds proves the rootless-first
// readiness path: the engine publishes the HTTP container port on
// 127.0.0.1 ephemeral, probes the recorded loopback bind (never a
// container IP), and records the binds for the active record + proxy.
func TestDeploy_Mockery_HTTPSuccessRecordsLoopbackBinds(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18080, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.MatchedBy(func(claims []domain.AppListenerReservation) bool {
		return len(claims) == 1 && claims[0].Port == 18080 && claims[0].Owner == domain.OwnerGordonBackend && claims[0].ContainerID == "c-new"
	})).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(a domain.AppActive) bool {
		return a.Services["web"].BackendBinds[8080] == 18080
	})).Return(nil).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)

	var probedURL string
	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets},
		zerowrap.Default(),
	).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(_ context.Context, url string) (int, error) { probedURL = url; return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, map[int]int{8080: 18080}, result.Services["web"].BackendBinds)
	assert.Contains(t, probedURL, "127.0.0.1:18080", "readiness dials the loopback bind, never a container IP")
	runtime.AssertNotCalled(t, "GetContainerNetworkInfo", mock.Anything, mock.Anything)
}

// TestDeploy_Mockery_HTTPRetiresOldAfterPublish proves retire-after-publish
// ordering: the replaced container is stopped+removed only after the new
// effective state is published (proxy already switched), and a retire
// failure records a cleanup warning without flipping the outcome.
func TestDeploy_Mockery_HTTPRetiresOldAfterPublish(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old"},
		},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18080, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.MatchedBy(func(claims []domain.AppListenerReservation) bool {
		return len(claims) == 1 && claims[0].Port == 18080 && claims[0].Owner == domain.OwnerGordonBackend && claims[0].ContainerID == "c-new"
	})).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old"},
		},
	}, true, nil)
	var publishedAt, retiredAt int
	var step int
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(func(context.Context, domain.AppActive) error {
		step++
		publishedAt = step
		return nil
	}).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old").RunAndReturn(func(context.Context, string) error {
		step++
		retiredAt = step
		return nil
	}).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-old").Return(nil).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	// The superseded generation stops being recovery-inhibited only
	// once the replacement is published.
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()

	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets},
		zerowrap.Default(),
	).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, "c-old", result.Services["web"].Retire)
	assert.Greater(t, retiredAt, publishedAt, "retire runs after publication")
	assert.Empty(t, result.CleanupWarnings)
}

func TestDeploy_Mockery_HTTPFailureKeepsOld(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old"},
		},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	// Replacement created + started; readiness fails fast.
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18080, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.MatchedBy(func(claims []domain.AppListenerReservation) bool {
		return len(claims) == 1 && claims[0].Port == 18080 && claims[0].Owner == domain.OwnerGordonBackend && claims[0].ContainerID == "c-new"
	})).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", true).Return(nil).Once()
	runtime.EXPECT().GetContainerLogs(mock.Anything, "c-new", false).Return(io.NopCloser(strings.NewReader("boom\n")), nil).Once()

	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets},
		zerowrap.Default(),
	).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 500, nil },
		func(context.Context, string) error { return errors.New("refused") },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.NotContains(t, result.Services["web"].Error, "logs:", "raw logs must never be embedded in the public error")
	require.Len(t, result.Services["web"].Diagnostics, 1, "failure diagnostics are kept separately")
	assert.Equal(t, "boom", result.Services["web"].Diagnostics[0])
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old")
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-old", false)
	runtime.AssertNotCalled(t, "RemoveVolume", mock.Anything, mock.Anything, mock.Anything)
}

func TestDeploy_Mockery_InterruptedVolumeMarksUnsafe(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	// The GC barrier must be held across resource acquisition through
	// the durable ownership publication that protects the volume.
	barrier := outmocks.NewMockGCBarrier(t)
	gcLease := outmocks.NewMockGCLease(t)
	gcLeaseHeld := false
	ownershipSaves := 0
	barrier.EXPECT().AcquireShared(mock.Anything).Run(func(context.Context) {
		gcLeaseHeld = true
	}).Return(gcLease, nil).Once()
	gcLease.EXPECT().Release().Run(func() {
		gcLeaseHeld = false
	}).Once()

	svcSpec := webService()
	svcSpec.HTTP = nil
	svcSpec.TCP = []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "9000"}}
	svcSpec.Readiness = domain.AppReadiness{Type: "none", Timeout: time.Second}
	svcSpec.Volumes = []domain.AppVolume{{Name: "d", Path: "/data"}}
	svcSpec.Secrets = map[string]string{}
	rev := domain.AppDesiredRevision{
		Revision: "rev-1", App: "blog",
		Spec: domain.AppSpec{Name: "blog", Env: map[string]string{}, Services: []domain.AppService{svcSpec}},
	}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, svcSpec.Image).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	runtime.EXPECT().InspectImageVolumes(mock.Anything, svcSpec.Image).Return(nil, nil)
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-uuid-blog", Volumes: []domain.AppOwnedVolume{{Name: "d", Service: "web", RuntimeName: "gordon-blog--web--vol--d", State: domain.AppResourceAttached}}}, nil).Times(4)
	runtime.EXPECT().VolumeExists(mock.Anything, "gordon-blog--web--vol--d").Return(true, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old"},
		},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	runtime.EXPECT().StopContainer(mock.Anything, "c-old").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	expectNetworkProvision(runtime, "app-uuid-blog", 1)
	// The volume-owning replacement must be inhibited before it can
	// write, and released only after the new generation is published.
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.MatchedBy(func(inhibition domain.AppRecoveryInhibition) bool {
		return inhibition.App == "blog" && inhibition.Service == "web" &&
			inhibition.ContainerID == "c-old" && inhibition.Reason == domain.AppInhibitReplacementPending
	})).Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()
	runtime.EXPECT().CreateVolume(mock.Anything, "gordon-blog--web--vol--d", mock.MatchedBy(func(labels map[string]string) bool {
		return labels[domain.LabelApp] == "blog" &&
			labels[domain.LabelAppID] == "app-uuid-blog" &&
			labels[domain.LabelAppService] == "web" &&
			labels[domain.LabelAppRevision] == "rev-1"
	})).Run(func(context.Context, string, map[string]string) {
		require.True(t, gcLeaseHeld, "volume creation must run inside the shared GC lease")
		// The durable ownership reservation must already be written:
		// a crash after this point leaves a protected record, never an
		// unowned volume that prune could later adopt.
		require.GreaterOrEqual(t, ownershipSaves, 1,
			"ownership must be reserved before the runtime volume exists")
	}).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 9000, HostPort: 19000, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.MatchedBy(func(claims []domain.AppListenerReservation) bool {
		return len(claims) == 1 && claims[0].Port == 19000 && claims[0].Owner == domain.OwnerGordonBackend && claims[0].ContainerID == "c-new"
	})).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{}}, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()
	restartUnsafeSaved := false
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil).Run(func(_ context.Context, ownership domain.AppOwnership) {
		require.True(t, gcLeaseHeld, "ownership publication must run inside the shared GC lease")
		ownershipSaves++
		if ownership.Services["web"].RestartUnsafe {
			restartUnsafeSaved = true
		}
	})

	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets},
		zerowrap.Default(),
	).WithProbeDeps(deployment.NewProbeDeps(runtime)).WithGCBarrier(barrier)

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"web"}, result.Interrupted)
	assert.True(t, result.Services["web"].RestartUnsafe)
	assert.True(t, restartUnsafeSaved, "the terminal ownership write must record the restart-unsafe service")
	assert.GreaterOrEqual(t, ownershipSaves, 2, "the volume reservation and the terminal ownership write must both be durable")
	assert.False(t, gcLeaseHeld, "the shared GC lease must be released after publication")
	runtime.AssertNotCalled(t, "RemoveVolume", mock.Anything, mock.Anything, mock.Anything)
}

// TestDeploy_Mockery_MixedTCPUDPPublishesBothLoopbacks proves a mixed
// TCP+UDP service publishes both protocols on 127.0.0.1 ephemeral,
// resolves them in one grouped inspection (same container port number
// on both protocols yields two distinct binds), and persists both maps
// with exact-protocol claims.
func TestDeploy_Mockery_MixedTCPUDPPublishesBothLoopbacks(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	svcSpec := webService()
	svcSpec.HTTP = nil
	svcSpec.TCP = []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "9000"}}
	svcSpec.UDP = []domain.AppUDPInterface{{Entrypoint: "udp", Port: 9000, Publish: "9000"}}
	svcSpec.Readiness = domain.AppReadiness{Type: "none", Timeout: time.Second}
	svcSpec.Secrets = map[string]string{}
	rev := domain.AppDesiredRevision{
		Revision: "rev-1", App: "blog",
		Spec: domain.AppSpec{Name: "blog", Env: map[string]string{}, Services: []domain.AppService{svcSpec}},
	}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, svcSpec.Image).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	runtime.EXPECT().InspectImageVolumes(mock.Anything, svcSpec.Image).Return(nil, nil)
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	expectNetworkProvision(runtime, "app-blog", 1)

	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		if len(cfg.PortPublishes) != 2 {
			return false
		}
		byProto := map[domain.NetworkProtocol]domain.ContainerPortPublish{}
		for _, publish := range cfg.PortPublishes {
			if publish.HostIP != "127.0.0.1" || publish.HostPort != 0 || publish.ContainerPort != 9000 {
				return false
			}
			byProto[publish.Protocol] = publish
		}
		return len(byProto) == 2
	})).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.MatchedBy(func(ports []domain.ContainerBackendPort) bool {
		if len(ports) != 2 {
			return false
		}
		seen := map[domain.ContainerBackendPort]bool{}
		for _, port := range ports {
			seen[port] = true
		}
		return seen[domain.ContainerBackendPort{ContainerPort: 9000, Protocol: domain.NetworkProtocolTCP}] &&
			seen[domain.ContainerBackendPort{ContainerPort: 9000, Protocol: domain.NetworkProtocolUDP}]
	})).Return([]domain.ContainerBackendBind{
		{ContainerPort: 9000, HostPort: 19000, Protocol: domain.NetworkProtocolTCP},
		{ContainerPort: 9000, HostPort: 19001, Protocol: domain.NetworkProtocolUDP},
	}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.MatchedBy(func(claims []domain.AppListenerReservation) bool {
		if len(claims) != 2 {
			return false
		}
		byProto := map[string]domain.AppListenerReservation{}
		for _, claim := range claims {
			if claim.Owner != domain.OwnerGordonBackend || claim.ContainerID != "c-new" || claim.IP != "127.0.0.1" {
				return false
			}
			byProto[claim.Proto] = claim
		}
		return byProto["tcp"].Port == 19000 && byProto["udp"].Port == 19001
	})).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{}}, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(a domain.AppActive) bool {
		svc := a.Services["web"]
		return svc.BackendBinds[9000] == 19000 && svc.UDPBackendBinds[9000] == 19001
	})).Return(nil).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil).Once()

	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets},
		zerowrap.Default(),
	).WithProbeDeps(deployment.NewProbeDeps(runtime))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, map[int]int{9000: 19000}, result.Services["web"].BackendBinds)
	assert.Equal(t, map[int]int{9000: 19001}, result.Services["web"].UDPBackendBinds)
}

// TestDeploy_Mockery_InjectsAppEnvAndSecrets proves the captured revision's
// app-wide public env reaches every service container alongside that
// service's own resolved secrets, sorted and deterministic. Public-only
// and empty-value cases are covered; resolved secret values never enter
// saved state (SaveActive/SaveOwnership carry no KEY=value payloads).
func TestDeploy_Mockery_InjectsAppEnvAndSecrets(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	svcSpec := webService()
	svcSpec.HTTP = nil
	svcSpec.Readiness = domain.AppReadiness{}
	rev := domain.AppDesiredRevision{
		Revision: "rev-1", App: "blog",
		Spec: domain.AppSpec{
			Name: "blog",
			Env:  map[string]string{"APP_ENV": "production", "EMPTY_OK": ""},
			Services: []domain.AppService{
				svcSpec,
				{
					Name: "worker", Image: "docker.io/example/worker:1.4.2",
					StopGrace: 10 * time.Second,
					Readiness: domain.AppReadiness{Timeout: 30 * time.Second},
					Secrets:   map[string]string{},
				},
			},
		},
	}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[1].Image).Return("sha256:"+strings.Repeat("b", 64), nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("s3cr3t", nil).Twice()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, rev.Spec.Services[0].Image).Return(nil, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, rev.Spec.Services[1].Image).Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	expectNetworkProvision(runtime, "app-blog", 2)

	var webEnv, workerEnv []string
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		if len(cfg.Env) != 3 {
			return false
		}
		webEnv = append([]string(nil), cfg.Env...)
		return true
	})).Return(&domain.Container{ID: "c-web", Name: "web"}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		workerEnv = append([]string(nil), cfg.Env...)
		return len(cfg.Env) == 2
	})).Return(&domain.Container{ID: "c-worker", Name: "worker"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(a domain.AppActive) bool {
		for _, svc := range a.Services {
			for _, kv := range []string{svc.Image, svc.Digest, svc.Container} {
				if kv == "s3cr3t" || kv == "DATABASE_URL=s3cr3t" {
					return false
				}
			}
		}
		return true
	})).Return(nil)
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets},
		zerowrap.Default(),
	).WithProbeDeps(deployment.NewProbeDeps(runtime))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"APP_ENV=production", "DATABASE_URL=s3cr3t", "EMPTY_OK="}, webEnv)
	assert.Equal(t, []string{"APP_ENV=production", "EMPTY_OK="}, workerEnv)
}

// TestStart_RunningContainerRefreshesShiftedBinds proves boot/start bind
// verification: a running container whose ephemeral loopback bind shifted
// (native restart while the daemon was away) gets its ACTIVE record
// updated before the proxy can dial the stale bind. The ACTIVE
// container is never stopped or removed by re-inspection.
func TestStart_RunningContainerRefreshesShiftedBinds(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil)
	active := domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1",
				Image:             rev.Spec.Services[0].Image,
				Digest:            "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Container:         "c-old",
				Spec:              rev.Spec.Services[0],
				BackendBinds:      map[int]int{8080: 32770},
			},
		},
	}
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "c-old").Return(true, nil)
	// Native restart shifted the ephemeral bind: 32770 -> 32771.
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-old", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.MatchedBy(func(claims []domain.AppListenerReservation) bool {
		return len(claims) == 1 && claims[0].Port == 32771 && claims[0].Owner == domain.OwnerGordonBackend && claims[0].ContainerID == "c-old"
	})).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(a domain.AppActive) bool {
		return a.Services["web"].BackendBinds[8080] == 32771
	})).Return(nil).Once()

	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets},
		zerowrap.Default(),
	).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))
	result, err := svc.Start(ctx, "blog", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Contains(t, result.Services, "web")
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, map[int]int{8080: 32771}, result.Services["web"].BackendBinds)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

// TestStart_BindVerificationFailureFailsClosed proves an unverified bind
// is never served: when re-inspection fails on a running container, the
// service step fails (fail-closed) without touching the container, and
// the stale recorded bind is withdrawn from ACTIVE so the next traffic
// rebuild cannot re-expose a dead or recycled loopback port.
func TestStart_BindVerificationFailureFailsClosed(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil)
	active := domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1",
				Image:             rev.Spec.Services[0].Image,
				Digest:            "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Container:         "c-old",
				Spec:              rev.Spec.Services[0],
				BackendBinds:      map[int]int{8080: 32770},
			},
		},
	}
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "c-old").Return(true, nil)
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-old", mock.Anything).Return(nil, assert.AnError).Once()

	traffic := &recordingTraffic{}
	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic},
		zerowrap.Default(),
	)
	result, err := svc.Start(ctx, "blog", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	// The stale bind is withdrawn through the canonical boundary
	// (fail-closed projection); deployment writes no ACTIVE binds itself.
	assert.Equal(t, []string{"blog/web", "blog/web"}, traffic.withdrawn())
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
}

// TestRestart_MixedServiceRefreshesBothProtocols proves a runtime restart
// re-inspects TCP and UDP binds together: shifted ephemeral ports on
// both protocols persist to ACTIVE and surface in the result.
func TestRestart_MixedServiceRefreshesBothProtocols(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	svcSpec := webService()
	svcSpec.HTTP = nil
	svcSpec.TCP = []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "9000"}}
	svcSpec.UDP = []domain.AppUDPInterface{{Entrypoint: "udp", Port: 9000, Publish: "9000"}}
	svcSpec.Readiness = domain.AppReadiness{Type: "none", Timeout: time.Second}
	svcSpec.Secrets = map[string]string{}
	active := domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				EffectiveRevision: "rev-1",
				Image:             svcSpec.Image,
				Digest:            "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Container:         "c-old",
				Spec:              svcSpec,
				BackendBinds:      map[int]int{9000: 32770},
				UDPBackendBinds:   map[int]int{9000: 32780},
			},
		},
	}
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().RestartContainer(mock.Anything, "c-old").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-old", mock.Anything).Return([]domain.ContainerBackendBind{
		{ContainerPort: 9000, HostPort: 32771, Protocol: domain.NetworkProtocolTCP},
		{ContainerPort: 9000, HostPort: 32781, Protocol: domain.NetworkProtocolUDP},
	}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.MatchedBy(func(claims []domain.AppListenerReservation) bool {
		if len(claims) != 2 {
			return false
		}
		byProto := map[string]int{}
		for _, claim := range claims {
			byProto[claim.Proto] = claim.Port
		}
		return byProto["tcp"] == 32771 && byProto["udp"] == 32781
	})).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(a domain.AppActive) bool {
		svc := a.Services["web"]
		return svc.BackendBinds[9000] == 32771 && svc.UDPBackendBinds[9000] == 32781
	})).Return(nil).Once()

	svc := deployment.NewService(
		deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets},
		zerowrap.Default(),
	)
	result, err := svc.Restart(ctx, "blog", "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, map[int]int{9000: 32771}, result.Services["web"].BackendBinds)
	assert.Equal(t, map[int]int{9000: 32781}, result.Services["web"].UDPBackendBinds)
}
