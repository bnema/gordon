package deployment_test

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/appstate"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/deployment"
)

// seedUnchangedApp seeds an app whose ACTIVE service runs exactly what the
// desired revision would create: same image, digest, spec, and env.
func seedUnchangedApp(t *testing.T, ctx context.Context, store *appstate.Store) {
	t.Helper()
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: time.Second}
	activeRev := testRevision("blog", spec)
	activeRev.Revision = "rev-0"
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {
			Container: "c-old", EffectiveRevision: "rev-0", Image: spec.Image,
			Digest: restartTestDigest, Spec: activeRev.Spec.Services[0], BackendBinds: map[int]int{8080: 18080},
		},
	}}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(t, ctx, store, "intent-0", "", activeRev)
	seedRevision(t, ctx, store, "intent-1", "rev-0", testRevision("blog", spec))
}

// unchangedDeployService wires a deploy of an app whose ACTIVE service
// already matches the desired revision.
func unchangedDeployService(t *testing.T) (*deployment.Service, *outmocks.MockContainerRuntime, *outmocks.MockImageResolver, context.Context, interface {
	LoadActive(context.Context, string) (domain.AppActive, bool, error)
	LoadOperation(context.Context, string, string) (domain.AppOperation, error)
}) {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedUnchangedApp(t, ctx, store)
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/web:1.4.2").Return(restartTestDigest, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/web:1.4.2").Return(nil, nil).Once()
	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))
	return svc, runtime, images, ctx, store
}

// TestDeploy_SkipsServiceAlreadyRunningTheSameImage proves a deploy whose
// resolved digest, spec, and env match the running service keeps the
// container and reports it unchanged instead of recreating it.
func TestDeploy_SkipsServiceAlreadyRunningTheSameImage(t *testing.T) {
	svc, runtime, _, ctx, store := unchangedDeployService(t)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").
		Return(&domain.Container{ID: "c-old", Status: string(domain.ContainerStatusRunning)}, nil).Once()

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "op-same"})
	require.NoError(t, err)
	assert.Equal(t, deployment.ServiceResultUnchanged, result.Services["web"].Result)
	assert.Equal(t, "c-old", result.Services["web"].After, "the running container is kept")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)

	op, err := store.LoadOperation(ctx, "blog", "op-same")
	require.NoError(t, err)
	assert.Equal(t, domain.AppOutcomeSuccess, op.Outcome)
	step, found := opStep(op, "service.web.replace")
	require.True(t, found)
	assert.Equal(t, domain.AppStepSucceeded, step.State)
	assert.Contains(t, step.Detail, "unchanged: already running docker.io/example/web:1.4.2")

	active, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "c-old", active.Services["web"].Container)
	assert.True(t, active.Converged, "an unchanged deploy still converges ACTIVE on the deployed revision")
}

// TestDeploy_ReplacesWhenTheRunningContainerIsNotRunning proves the skip
// never trusts ACTIVE alone: a stopped or missing container is replaced.
func TestDeploy_ReplacesWhenTheRunningContainerIsNotRunning(t *testing.T) {
	svc, runtime, _, ctx, _ := unchangedDeployService(t)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").
		Return(&domain.Container{ID: "c-old", Status: "exited"}, nil).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "op-exited"})
	require.NoError(t, err)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, "c-new", result.Services["web"].After)
}

// TestDeploy_ReplacesWhenTheDigestChanged proves a new image digest behind
// the same tag is always deployed.
func TestDeploy_ReplacesWhenTheDigestChanged(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedUnchangedApp(t, ctx, store)
	newDigest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/web:1.4.2").Return(newDigest, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/web:1.4.2").Return(nil, nil).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "op-new-digest"})
	require.NoError(t, err)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	runtime.AssertNotCalled(t, "InspectContainer", mock.Anything, "c-old")

	active, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, newDigest, active.Services["web"].Digest)
}
