package deployment_test

import (
	"context"
	"errors"
	"slices"
	"sync"
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

// restartTrafficRecorder records the traffic boundary calls of a restart.
type restartTrafficRecorder struct {
	mu          sync.Mutex
	events      *[]string
	failRebuild bool
}

func (r *restartTrafficRecorder) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.events != nil {
		*r.events = append(*r.events, event)
	}
}
func (r *restartTrafficRecorder) RebuildTraffic(context.Context) error {
	r.record("traffic")
	if r.failRebuild {
		return errors.New("traffic rebuild failed")
	}
	return nil
}
func (r *restartTrafficRecorder) WithdrawService(context.Context, string, string) error {
	r.record("withdraw")
	return nil
}
func (r *restartTrafficRecorder) WithdrawServiceState(context.Context, string, string) error {
	r.record("withdraw-state")
	return nil
}

const restartTestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestRestart_RecreatesFromPinnedDigestAndRereadsSecrets proves the restart
// contract: the service is withdrawn, the running container is retired, and
// a NEW container is created from the digest pinned in ACTIVE, with the
// current secret values in its environment. The image is never re-resolved.
func TestRestart_RecreatesFromPinnedDigestAndRereadsSecrets(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	spec := webService()
	spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: time.Second}
	rev := testRevision("blog", spec)
	rev.Revision = "rev-0"
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-old", EffectiveRevision: "rev-0", Image: spec.Image, Digest: restartTestDigest, Spec: rev.Spec.Services[0], BackendBinds: map[int]int{8080: 18080}},
	}}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(t, ctx, store, "intent-0", "", rev)

	var order []string
	secrets.EXPECT().GetSecret(mock.Anything, mock.Anything).Return("rotated-value", nil).Twice()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).RunAndReturn(
		func(context.Context, string, time.Duration) error { recordEvent(&order, "stop-old"); return nil }).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	var created *domain.ContainerConfig
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, cfg *domain.ContainerConfig) (*domain.Container, error) {
			created = cfg
			recordEvent(&order, "create-new")
			return &domain.Container{ID: "c-new", Name: "web"}, nil
		}).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18082, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &restartTrafficRecorder{events: &order},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Restart(ctx, "blog", "web", "op-restart")
	require.NoError(t, err)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, "c-new", result.Services["web"].After, "restart creates a new container")
	require.NotNil(t, created)
	assert.Contains(t, created.Env, "DATABASE_URL=rotated-value", "the new container reads the current secret value")
	assert.Less(t, slices.Index(order, "withdraw"), slices.Index(order, "stop-old"), "traffic is withdrawn before the old container stops")
	assert.Less(t, slices.Index(order, "stop-old"), slices.Index(order, "create-new"), "the old generation is gone before the new one exists")
	images.AssertNotCalled(t, "ResolveDigest", mock.Anything, mock.Anything)

	active, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "c-new", active.Services["web"].Container)
	assert.Equal(t, restartTestDigest, active.Services["web"].Digest, "the pinned digest is reused, never re-resolved")
}

// TestRestart_ReadinessFailureFailsTheRestart proves a recreated container
// that never becomes ready is not served: the restart fails and the service
// is not published on the new container.
func TestRestart_ReadinessFailureFailsTheRestart(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedReplaceableApp(t, ctx, store)

	// The old generation is retired exactly once before the candidate
	// exists; the unready candidate is then removed.
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-new", mock.Anything).Return(nil).Maybe()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", mock.Anything).Return(nil).Maybe()
	runtime.EXPECT().InspectContainer(mock.Anything, mock.Anything).Return(nil, domain.ErrContainerNotFound).Maybe()
	runtime.EXPECT().GetContainerLogs(mock.Anything, mock.Anything, mock.Anything).Return(nil, domain.ErrContainerNotFound).Maybe()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18082, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 503, nil },
		func(context.Context, string) error { return assert.AnError },
	))

	result, err := svc.Restart(ctx, "blog", "web", "op-restart-unready")
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)

	active, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.NotEqual(t, "c-new", active.Services["web"].Container, "an unready container is never published")

	op, err := store.LoadOperation(ctx, "blog", "op-restart-unready")
	require.NoError(t, err)
	step, found := opStep(op, "service.web.restart")
	require.True(t, found)
	assert.Equal(t, domain.AppStepFailed, step.State)
	assert.Equal(t, "c-new", step.After, "the failed candidate stays journaled for reconciliation")
}

// TestRestart_MissingSecretLeavesTheRunningContainerUntouched proves every
// check that can fail runs before the old container is retired: a secret
// that cannot be read fails the restart while the service keeps running.
func TestRestart_MissingSecretLeavesTheRunningContainerUntouched(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	spec := webService()
	rev := testRevision("blog", spec)
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-old", EffectiveRevision: rev.Revision, Image: spec.Image, Digest: restartTestDigest, Spec: rev.Spec.Services[0], BackendBinds: map[int]int{8080: 18080}},
	}}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(t, ctx, store, "intent-0", "", rev)

	secrets.EXPECT().GetSecret(mock.Anything, mock.Anything).Return("", assert.AnError).Once()
	// The failure path checks whether the recorded container is gone before
	// clearing its inhibition; it is still running, so nothing is cleared.
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").
		Return(&domain.Container{ID: "c-old", Status: string(domain.ContainerStatusRunning)}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())

	result, err := svc.Restart(ctx, "blog", "web", "op-restart-secret")
	require.Error(t, err)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.ErrorContains(t, err, "restart failed for web")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)

	active, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "c-old", active.Services["web"].Container, "the running container stays published")
	assert.Equal(t, map[int]int{8080: 18080}, active.Services["web"].BackendBinds)
}
