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

// unchangedSeed shapes the ACTIVE record and the desired revision of an app
// whose defaults match exactly (same image, digest, spec, env, binds).
type unchangedSeed struct {
	binds       map[int]int
	desiredEnv  map[string]string
	inhibitedBy string
}

// seedUnchangedApp seeds an app whose ACTIVE service runs exactly what the
// desired revision would create, unless the seed alters one input.
func seedUnchangedApp(t *testing.T, ctx context.Context, store *appstate.Store, seeds ...unchangedSeed) {
	t.Helper()
	seed := unchangedSeed{binds: map[int]int{8080: 18080}}
	if len(seeds) > 0 {
		seed = seeds[0]
	}
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: time.Second}
	activeRev := testRevision("blog", spec)
	activeRev.Revision = "rev-0"
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {
			Container: "c-old", EffectiveRevision: "rev-0", Image: spec.Image, ActivatedBy: "op-created",
			Digest: restartTestDigest, Spec: activeRev.Spec.Services[0], BackendBinds: seed.binds,
		},
	}}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(t, ctx, store, "intent-0", "", activeRev)
	desired := testRevision("blog", spec)
	if seed.desiredEnv != nil {
		desired.Spec.Env = seed.desiredEnv
	}
	seedRevision(t, ctx, store, "intent-1", "rev-0", desired)
	if seed.inhibitedBy != "" {
		require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
			App: "blog", Service: "web", ContainerID: "c-old", Reason: domain.AppInhibitReplacementPending, Operation: seed.inhibitedBy,
		}))
	}
}

// expectReplacement wires one full replacement of c-old by c-new.
func expectReplacement(runtime *outmocks.MockContainerRuntime) {
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
}

// unchangedDeployService wires a deploy of an app whose ACTIVE service
// already matches the desired revision.
func unchangedDeployService(t *testing.T, seeds ...unchangedSeed) (*deployment.Service, *outmocks.MockContainerRuntime, *outmocks.MockImageResolver, context.Context, *appstate.Store) {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedUnchangedApp(t, ctx, store, seeds...)
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
	assert.Equal(t, map[int]int{8080: 18080}, active.Services["web"].BackendBinds, "the kept container stays routable")
	assert.Equal(t, "op-created", active.Services["web"].ActivatedBy, "a kept container keeps its original activation")
	assert.True(t, active.Converged, "an unchanged deploy still converges ACTIVE on the deployed revision")
}

// TestDeploy_ReplacesSameDigestWhenTheServiceIsNotServing proves the skip
// never keeps a container that is not fully published: missing routing
// binds (a withdrawn generation), a recovery inhibition left by a failed
// replacement, or a changed app env all force a replacement.
func TestDeploy_ReplacesSameDigestWhenTheServiceIsNotServing(t *testing.T) {
	tests := []struct {
		name string
		seed unchangedSeed
	}{
		{name: "withdrawn binds", seed: unchangedSeed{}},
		{name: "recovery inhibited", seed: unchangedSeed{binds: map[int]int{8080: 18080}, inhibitedBy: "op-failed"}},
		{name: "app env changed", seed: unchangedSeed{binds: map[int]int{8080: 18080}, desiredEnv: map[string]string{"MODE": "prod"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, runtime, _, ctx, _ := unchangedDeployService(t, tc.seed)
			expectReplacement(runtime)

			result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "op-replace"})
			require.NoError(t, err)
			assert.Equal(t, "deployed", result.Services["web"].Result)
			assert.Equal(t, "c-new", result.Services["web"].After)
		})
	}
}

// TestDeploy_ReplacesWhenTheRunningContainerIsNotRunning proves the skip
// never trusts ACTIVE alone: a stopped or missing container is replaced.
func TestDeploy_ReplacesWhenTheRunningContainerIsNotRunning(t *testing.T) {
	svc, runtime, _, ctx, _ := unchangedDeployService(t)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").
		Return(&domain.Container{ID: "c-old", Status: "exited"}, nil).Once()
	expectReplacement(runtime)

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

// TestDeploy_RecreatesWhenASecretValueChanged proves deploy applies new
// secret values: a running container created with an old value is replaced,
// and one created with the current value is kept.
func TestDeploy_RecreatesWhenASecretValueChanged(t *testing.T) {
	tests := []struct {
		name       string
		runningEnv []string
		wantResult string
	}{
		{name: "secret changed", runningEnv: []string{"DB_PASSWORD=old", "PATH=/bin"}, wantResult: "deployed"},
		{name: "secret current", runningEnv: []string{"DB_PASSWORD=new", "PATH=/bin"}, wantResult: deployment.ServiceResultUnchanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, runtime, secrets, ctx := secretDeployService(t)
			secrets.EXPECT().GetSecret(mock.Anything, mock.Anything).Return("new", nil)
			runtime.EXPECT().InspectContainer(mock.Anything, "c-old").
				Return(&domain.Container{ID: "c-old", Status: string(domain.ContainerStatusRunning), Env: tc.runningEnv}, nil).Once()
			if tc.wantResult == "deployed" {
				expectReplacement(runtime)
			}

			result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "op-secret"})
			require.NoError(t, err)
			assert.Equal(t, tc.wantResult, result.Services["web"].Result)
		})
	}
}

// secretDeployService wires an unchanged app whose service reads one secret.
func secretDeployService(t *testing.T) (*deployment.Service, *outmocks.MockContainerRuntime, *outmocks.MockSecretProvider, context.Context) {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	spec := webService()
	spec.Secrets = map[string]string{"DB_PASSWORD": "db-password"}
	spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: time.Second}
	activeRev := testRevision("blog", spec)
	activeRev.Revision = "rev-0"
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {
			Container: "c-old", EffectiveRevision: "rev-0", Image: spec.Image, ActivatedBy: "op-created",
			Digest: restartTestDigest, Spec: activeRev.Spec.Services[0], BackendBinds: map[int]int{8080: 18080},
		},
	}}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(t, ctx, store, "intent-0", "", activeRev)
	seedRevision(t, ctx, store, "intent-1", "rev-0", testRevision("blog", spec))
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/web:1.4.2").Return(restartTestDigest, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/web:1.4.2").Return(nil, nil).Once()
	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))
	return svc, runtime, secrets, ctx
}
