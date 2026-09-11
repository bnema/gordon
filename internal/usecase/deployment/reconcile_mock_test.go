package deployment_test

import (
	"context"
	"fmt"
	"io"
	"strings"
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

// recordingTraffic is a fail-closed traffic boundary double: it records
// withdrawals and publications and can fail either operation.
type recordingTraffic struct {
	mu           sync.Mutex
	attempts     int
	withdrawals  []string
	rebuilds     int
	failWithdraw bool
	failRebuild  bool
}

func (t *recordingTraffic) WithdrawService(_ context.Context, app, service string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts++
	if t.failWithdraw {
		return assert.AnError
	}
	t.withdrawals = append(t.withdrawals, app+"/"+service)
	return nil
}

func (t *recordingTraffic) WithdrawServiceState(_ context.Context, app, service string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failWithdraw {
		return assert.AnError
	}
	t.withdrawals = append(t.withdrawals, app+"/"+service)
	return nil
}

func (t *recordingTraffic) withdrawAttempts() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

func (t *recordingTraffic) RebuildTraffic(context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failRebuild {
		return assert.AnError
	}
	t.rebuilds++
	return nil
}

func (t *recordingTraffic) withdrawn() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.withdrawals...)
}

func (t *recordingTraffic) rebuildCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.rebuilds
}

// recoveringService builds the engine with a recording traffic boundary
// and injectable L4 probes so recovery passes stay deterministic.
func recoveringService(
	t *testing.T,
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	traffic *recordingTraffic,
	httpGet func(context.Context, string) (int, error),
) *deployment.Service {
	t.Helper()
	return deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Traffic: traffic,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		httpGet,
		func(context.Context, string) error { return nil },
	))
}

// headlessSpec is a service with no interfaces and no readiness, so a
// recovery pass performs no bind inspection and no probe.
func headlessSpec() domain.AppService {
	return domain.AppService{Name: "web", Image: "registry.example.com/blog/web:1.4.2"}
}

func TestReconcileRunning_EmptyActiveTakesNoRuntimeAction(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{}, false, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "InspectContainer", mock.Anything, mock.Anything)
	assert.Zero(t, traffic.rebuildCount())
}

func TestReconcileRunning_StoppedIntentStopsRevivedExactIDWithoutRemoval(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog", Stopped: true}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running"}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-1").Return(nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	assert.GreaterOrEqual(t, traffic.rebuildCount(), 1, "a stopped app is republished without its backend")
}

func TestReconcileRunning_AbstainsWhenStoppedContainerAlreadyDown(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog", Stopped: true}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited"}, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
}

func TestReconcileRunning_StartsExitedExactIDThenVerifiesAndPublishes(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}
	spec := webService()
	spec.Readiness.Timeout = time.Second

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: spec, BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Twice()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	startedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited", StartedAt: startedAt}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: startedAt}, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-1", mock.Anything).
		Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.MatchedBy(func(claims []domain.AppListenerReservation) bool {
		return len(claims) == 1 && claims[0].Port == 32771 && claims[0].ContainerID == "c-1"
	})).Return(nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(a domain.AppActive) bool {
		return a.Services["web"].BackendBinds[8080] == 32771
	})).Return(nil).Once()

	var probedURL string
	svc := recoveringService(t, state, runtime, traffic, func(_ context.Context, url string) (int, error) {
		probedURL = url
		return 200, nil
	})
	require.NoError(t, svc.ReconcileRunning(ctx))

	assert.Contains(t, probedURL, "127.0.0.1:32771", "readiness follows the refreshed bind")
	assert.Equal(t, []string{"blog/web"}, traffic.withdrawn(), "the stale backend is withdrawn before recovery")
	assert.Equal(t, 1, traffic.rebuildCount())
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "PullImage", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

func TestReconcileRunning_ConfirmedMissingWithdrawsWithoutReconstruction(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(nil, fmt.Errorf("inspect: %w", domain.ErrContainerNotFound)).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrContainerNotFound)
	assert.Contains(t, err.Error(), "no reconstruction")
	assert.Equal(t, []string{"blog/web"}, traffic.withdrawn())
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "PullImage", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

func TestReconcileRunning_TransientInspectErrorTakesNoAction(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(nil, assert.AnError).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.NotErrorIs(t, err, domain.ErrContainerNotFound)
	assert.Empty(t, traffic.withdrawn(), "a transient error is not a dead backend")
	assert.Zero(t, traffic.rebuildCount())
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

func TestReconcileRunning_InhibitedGenerationIsNeverStarted(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-old", EffectiveRevision: "rev-1", Spec: headlessSpec(), BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return([]domain.AppRecoveryInhibition{{
		App: "blog", Service: "web", ContainerID: "c-old", Reason: domain.AppInhibitReplacementPending,
	}}, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recovery inhibited")
	runtime.AssertNotCalled(t, "InspectContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
	assert.Equal(t, []string{"blog/web"}, traffic.withdrawn(),
		"an inhibited generation must not stay published")
}

func TestReconcileRunning_NativeRestartRaceIsNotCharged(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Twice()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	startedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited", StartedAt: startedAt}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(assert.AnError).Once()
	// Native restart policy already revived it: the failed start is not
	// charged and no second restart is issued.
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: startedAt}, nil).Twice()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
	assert.Equal(t, 1, traffic.rebuildCount())
}

func TestReconcileRunning_NativeRestartRefreshesChangedBinds(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}
	spec := webService()
	spec.Readiness = domain.AppReadiness{Type: "none"}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: spec, BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Twice()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(&domain.Container{
		ID: "c-1", Status: "running",
		StartedAt: time.Date(2026, 9, 10, 12, 5, 0, 0, time.UTC),
	}, nil).Twice()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-1", mock.Anything).
		Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32799, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(a domain.AppActive) bool {
		return a.Services["web"].BackendBinds[8080] == 32799
	})).Return(nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
	assert.Equal(t, 1, traffic.rebuildCount(), "a changed bind is republished once")
}

func TestReconcileRunning_RestartingObservesAndRetries(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "restarting"}, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
}

func TestReconcileRunning_PausedRefusesNonDestructiveRecovery(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "paused"}, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "paused")
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

func TestReconcileRunning_OneFailureDoesNotBlockLaterApps(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	badActive := domain.AppActive{App: "bad", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-bad", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	bindSpec := webService()
	bindSpec.Readiness = domain.AppReadiness{Type: "none"}
	goodActive := domain.AppActive{App: "good", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-good", EffectiveRevision: "rev-1", Spec: bindSpec, BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"bad", "good"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "bad").Return(domain.AppStopIntent{App: "bad"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "good").Return(domain.AppStopIntent{App: "good"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "bad").Return(badActive, true, nil)
	state.EXPECT().LoadActive(mock.Anything, "good").Return(goodActive, true, nil).Twice()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "bad").Return(nil, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "good").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-bad").Return(nil, assert.AnError).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-good").Return(&domain.Container{
		ID: "c-good", Status: "running",
		StartedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}, nil)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-good").Return("", false, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-good", mock.Anything).
		Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"bad"`)
	runtime.AssertCalled(t, "InspectContainer", mock.Anything, "c-good")
	assert.Equal(t, 1, traffic.rebuildCount(), "the healthy app is still published")
}

func TestReconcileRunning_WithdrawalFailureBlocksRuntimeRecovery(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{failWithdraw: true}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec(), BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited"}, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "withdraw")
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything,
		"runtime recovery requires a proven fail-closed withdrawal")
	assert.Zero(t, traffic.rebuildCount())
}

func TestReconcileRunning_RetriesWithdrawalBeforeAnyLaterPublication(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{failWithdraw: true}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec(), BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited"}, nil).Times(3)
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	require.Error(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)

	// Recovery is retried on a later pass and must withdraw again before
	// it may start or publish anything.
	traffic.mu.Lock()
	traffic.failWithdraw = false
	traffic.mu.Unlock()
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertCalled(t, "StartContainer", mock.Anything, "c-1")
	assert.Equal(t, 2, traffic.withdrawAttempts(), "withdrawal is retried, never skipped")
}

func TestReconcileRunning_RebuildFailureDoesNotRestartHealthyContainer(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{failRebuild: true}

	bindSpec := webService()
	bindSpec.Readiness = domain.AppReadiness{Type: "none"}
	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: bindSpec, BackendBinds: map[int]int{8080: 32770}},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Twice()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(&domain.Container{
		ID: "c-1", Status: "running", StartedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}, nil).Twice()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-1", mock.Anything).
		Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

func TestReconcileRunning_UnhealthyRestartsOnlyAfterConsecutiveObservations(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	startedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: startedAt}, nil)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("unhealthy", true, nil)
	runtime.EXPECT().RestartContainer(mock.Anything, "c-1").Return(nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	// First observation only arms the streak.
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)

	// The second consecutive unhealthy observation restarts the exact ID.
	require.NoError(t, svc.ReconcileRunning(ctx))
	runtime.AssertCalled(t, "RestartContainer", mock.Anything, "c-1")
}

func TestReconcileRunning_HealthStartingWaits(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(&domain.Container{
		ID: "c-1", Status: "running", StartedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}, nil)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("starting", true, nil)

	svc := recoveringService(t, state, runtime, traffic, nil)
	for range 3 {
		require.NoError(t, svc.ReconcileRunning(ctx))
	}
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
}

func TestReconcileRunning_NoHealthcheckNeverRestartsOnReadiness(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}
	spec := webService()
	spec.Readiness.Timeout = 20 * time.Millisecond

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: spec, BackendBinds: map[int]int{8080: 32770}},
	}}
	firstExecution := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: firstExecution}, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil)
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-1", mock.Anything).
		Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32770, Protocol: domain.NetworkProtocolTCP}}, nil)
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil)

	// An execution unseen by this daemon is verified immediately because it
	// may have restarted while Gordon was absent. Readiness failure keeps it
	// withdrawn, but never restarts an otherwise running workload.
	svc := recoveringService(t, state, runtime, traffic, func(context.Context, string) (int, error) {
		return 500, nil
	})
	require.Error(t, svc.ReconcileRunning(ctx), "the unseen execution fails readiness and stays withdrawn")
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
}

func TestReconcileRunning_StaleGenerationIsNeverPublished(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	// A concurrent mutation replaced the generation while recovery held
	// the app lock: the reloaded ACTIVE names a different container.
	replaced := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-2", EffectiveRevision: "rev-2", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaced, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(&domain.Container{
		ID: "c-1", Status: "running", StartedAt: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC),
	}, nil).Twice()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "generation changed")
	assert.Zero(t, traffic.rebuildCount(), "stale recovery evidence must not publish")
}

func TestReconcileRunning_LogReadinessUsesTheExactExecutionScope(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}
	spec := headlessSpec()
	spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessLog, Contains: "ready", Timeout: time.Second}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: spec},
	}}
	startedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited", StartedAt: startedAt}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: startedAt}, nil).Twice()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()
	var observedID string
	var observedSince time.Time
	runtime.EXPECT().GetContainerLogsSince(mock.Anything, "c-1", mock.Anything, false).
		RunAndReturn(func(_ context.Context, containerID string, since time.Time, _ bool) (io.ReadCloser, error) {
			observedID = containerID
			observedSince = since
			return io.NopCloser(strings.NewReader("boot\nready\n")), nil
		}).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	require.NoError(t, svc.ReconcileRunning(ctx))
	assert.Equal(t, "c-1", observedID, "readiness reads the exact ACTIVE container")
	assert.Equal(t, startedAt, observedSince, "readiness is scoped to the current execution start")
	assert.Equal(t, 1, traffic.rebuildCount())
}

func TestReconcileRunning_OldExecutionMarkerCannotSatisfyReadiness(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}
	spec := headlessSpec()
	spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessLog, Contains: "ready", Timeout: 20 * time.Millisecond}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: spec},
	}}
	startedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited", StartedAt: startedAt}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: startedAt}, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()
	// The runtime only returns the current execution's logs; the old
	// marker is simply not part of the stream.
	runtime.EXPECT().GetContainerLogsSince(mock.Anything, "c-1", startedAt, false).
		Return(io.NopCloser(strings.NewReader("booting\n")), nil)

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "log readiness timeout")
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
}

// TestReconcileRunning_CrashLoopIsChargedEvenWhenStartsSucceed proves the
// crash-loop budget cannot be escaped by starts that succeed and then die
// before the next pass: after three unconfirmed interventions the
// generation is delayed instead of restarted every 15 seconds.
func TestReconcileRunning_CrashLoopIsChargedEvenWhenStartsSucceed(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil)
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil)
	// Every pass finds the exact ACTIVE ID exited again.
	started := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").Return(&domain.Container{
		ID: "c-1", Status: "exited", StartedAt: started,
	}, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Times(3)
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil)

	svc := recoveringService(t, state, runtime, traffic, nil)
	for pass := 1; pass <= 4; pass++ {
		require.NoError(t, svc.ReconcileRunning(ctx), "pass %d", pass)
	}
	runtime.AssertNumberOfCalls(t, "StartContainer", 3)
}

// TestReconcileRunning_StaleExecutionAfterReadinessNeverPublishes proves
// the last revalidation: a container that restarted while this pass
// waited for readiness must not republish the previous execution's binds.
func TestReconcileRunning_StaleExecutionAfterReadinessNeverPublishes(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	firstExecution := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited", StartedAt: firstExecution}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()
	// The workload restarted while the pass was verifying it.
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running", StartedAt: firstExecution.Add(time.Minute)}, nil).Once()

	svc := recoveringService(t, state, runtime, traffic, nil)
	err := svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "restarted during recovery")
	assert.Zero(t, traffic.rebuildCount(), "stale evidence must not publish")
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
}

// TestReconcileRunning_FailedReadinessPersistsNoBinds proves the ordering
// the plan requires: binds reach ACTIVE only after readiness passed, so a
// failed verification cannot be projected by any other publication.
func TestReconcileRunning_FailedReadinessPersistsNoBinds(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)
	traffic := &recordingTraffic{}
	spec := webService()
	spec.Readiness.Timeout = 20 * time.Millisecond

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: spec},
	}}
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	startedAt := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "exited", StartedAt: startedAt}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-1").Return(nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-1").Return("", false, nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-1", mock.Anything).
		Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()

	svc := recoveringService(t, state, runtime, traffic, func(context.Context, string) (int, error) {
		return 500, nil
	})
	require.Error(t, svc.ReconcileRunning(ctx))
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
	assert.Zero(t, traffic.rebuildCount())
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything)
}
