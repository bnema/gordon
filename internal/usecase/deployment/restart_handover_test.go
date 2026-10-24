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

func TestRestart_HoldsRestartsVerifiesAndRepublishes(t *testing.T) {
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
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").Return(&domain.Container{ID: "c-old", Status: "running", StartedAt: executionStartedAt}, nil).Maybe()
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
		State: state, Runtime: runtime, Images: images, Secrets: secrets, Handover: deployment.HandoverConfig{MaxHold: time.Minute},
		Traffic: &restartTrafficRecorder{events: &order},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(_ context.Context, url, _ string) (int, error) {
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
	assert.Equal(t, []string{"hold", "restart", "persist-binds", "ready", "traffic"}, order)
	assert.Equal(t, map[int]int{8080: 32771}, saved.Services["web"].BackendBinds)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

func TestRestart_HoldReadinessFailureReleasesClients(t *testing.T) {
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
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").Return(&domain.Container{ID: "c-old", Status: "running", StartedAt: executionStartedAt}, nil).Maybe()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-old", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets, Handover: deployment.HandoverConfig{MaxHold: time.Minute},
		Traffic: &restartTrafficRecorder{events: &order},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 503, nil },
		func(context.Context, string) error { return assert.AnError },
	))

	result, err := svc.Restart(ctx, "blog", "web", "")
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Contains(t, result.Services["web"].Error, "readiness")
	assert.Equal(t, "hold", order[0])
	assert.Contains(t, order, "withdraw", "the unverified generation is withdrawn from state")
	assert.Contains(t, order, "traffic", "the graph is republished fail-closed")
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}
