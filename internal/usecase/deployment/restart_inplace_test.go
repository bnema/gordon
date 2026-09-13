package deployment_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

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

func restartActive(t *testing.T, spec domain.AppService) domain.AppActive {
	t.Helper()
	return domain.AppActive{App: "blog", ConvergedRevision: "rev-1", Converged: true, Services: map[string]domain.AppEffectiveService{"web": {EffectiveRevision: "rev-1", ActivatedBy: "op-before", ActivatedAt: time.Now().Add(-time.Hour).UTC(), Image: spec.Image, Digest: restartTestDigest, Container: "c-old", Spec: spec, BackendBinds: map[int]int{8080: 32770}}}}
}

// TestRestart_InPlaceWithdrawsRestartsVerifiesAndRepublishes proves the
// restart contract: the service is withdrawn from traffic, the SAME pinned
// container is restarted, readiness is verified against its refreshed
// loopback bind, and traffic is republished. No second container is created
// and no container is stopped or removed.
func TestRestart_InPlaceWithdrawsRestartsVerifiesAndRepublishes(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.StopGrace = 10 * time.Millisecond
	// No declared readiness probe: the implicit HTTP check still applies to a
	// stateless public HTTP service on restart, exactly as it does on deploy.
	spec.Readiness = domain.AppReadiness{Timeout: time.Second}
	active := restartActive(t, spec)

	var order []string
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().RestartContainer(mock.Anything, "c-old", mock.Anything).RunAndReturn(
		func(context.Context, string, time.Duration) error {
			recordEvent(&order, "restart")
			return nil
		}).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-old", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	var saved domain.AppActive
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, refreshed domain.AppActive) error {
			saved = refreshed
			recordEvent(&order, "persist-binds")
			return nil
		}).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	var probedURL string
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &restartTrafficRecorder{events: &order},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(_ context.Context, url string) (int, error) {
			probedURL = url
			recordEvent(&order, "ready")
			return 200, nil
		},
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Restart(ctx, "blog", "web", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, "c-old", result.Services["web"].After, "the same pinned container is restarted in place")
	assert.Contains(t, probedURL, "127.0.0.1:32771", "readiness dials the refreshed loopback bind")
	assert.Equal(t, []string{"withdraw", "restart", "persist-binds", "ready", "traffic"}, order)
	assert.Equal(t, map[int]int{8080: 32771}, saved.Services["web"].BackendBinds)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

// TestRestart_ReadinessFailureLeavesTheRestartedContainerWithdrawn proves a
// restart is never served unverified: the failure leaves the same container
// in place but withdraws its recorded binds, and the graph is republished
// fail-closed. Nothing is created, stopped, or removed.
func TestRestart_ReadinessFailureLeavesTheRestartedContainerWithdrawn(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.StopGrace = 10 * time.Millisecond
	spec.Readiness = domain.AppReadiness{Timeout: 50 * time.Millisecond}
	active := restartActive(t, spec)

	var order []string
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().RestartContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-old", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &restartTrafficRecorder{events: &order},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 503, nil },
		func(context.Context, string) error { return assert.AnError },
	))

	result, err := svc.Restart(ctx, "blog", "web", "")
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Contains(t, result.Services["web"].Error, "readiness")
	assert.Contains(t, order, "withdraw-state", "the unverified generation is withdrawn from state")
	assert.Contains(t, order, "traffic", "the graph is republished fail-closed")
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}
