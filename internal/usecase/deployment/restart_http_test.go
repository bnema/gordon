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

func restartRevision(spec domain.AppService) domain.AppDesiredRevision {
	return domain.AppDesiredRevision{Revision: "rev-1", App: "blog", Spec: domain.AppSpec{Name: "blog", Env: map[string]string{}, Services: []domain.AppService{spec}}}
}

// cloneActive returns a copy of an ACTIVE record with its own service map,
// so a mocked LoadActive never hands the same mutable map to the code
// under test twice.
func cloneActive(a domain.AppActive) domain.AppActive {
	cloned := a
	cloned.Services = make(map[string]domain.AppEffectiveService, len(a.Services))
	for name, svc := range a.Services {
		cloned.Services[name] = svc
	}
	return cloned
}

func TestRestart_HTTPZeroDowntimeRepointsTrafficBeforeRetiringOld(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.StopGrace = 10 * time.Millisecond
	spec.Readiness.Timeout = time.Second
	active := restartActive(t, spec)
	rev := restartRevision(spec)
	oldActivatedAt := active.Services["web"].ActivatedAt
	var order []string
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	state.EXPECT().LoadRevision(mock.Anything, "blog", "rev-1").Return(rev, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	var saved domain.AppActive
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Run(func(_ context.Context, a domain.AppActive) { saved = a; order = append(order, "active") }).Return(nil).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Run(func(context.Context, string, time.Duration) { order = append(order, "stop-old") }).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-old").Return(nil).Once()
	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: &restartTrafficRecorder{events: &order}}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime, func(context.Context, string) (int, error) { return 200, nil }, func(context.Context, string) error { return nil }))
	result, err := svc.Restart(ctx, "blog", "web", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"active", "traffic", "stop-old", "traffic"}, order)
	assert.Equal(t, "c-new", saved.Services["web"].Container)
	assert.Equal(t, result.Op, saved.Services["web"].ActivatedBy)
	assert.True(t, saved.Services["web"].ActivatedAt.After(oldActivatedAt))
	assert.Equal(t, "rev-1", saved.ConvergedRevision)
	assert.True(t, saved.Converged)
}

func TestRestart_HTTPReadinessFailureKeepsOldServing(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.Readiness.Timeout = 50 * time.Millisecond
	active := restartActive(t, spec)
	rev := restartRevision(spec)
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	state.EXPECT().LoadRevision(mock.Anything, "blog", "rev-1").Return(rev, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	runtime.EXPECT().GetContainerLogs(mock.Anything, "c-new", false).Return(nil, assert.AnError).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", true).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-new").Return(nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	var events []string
	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: &restartTrafficRecorder{events: &events}}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime, func(context.Context, string) (int, error) { return 503, nil }, func(context.Context, string) error { return nil }))
	_, err := svc.Restart(ctx, "blog", "web", "")
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
	assert.NotContains(t, events, "withdraw")
}

func TestRestart_HTTPWithVolumeStaysInPlace(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}}
	active := restartActive(t, spec)
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	runtime.EXPECT().RestartContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-old", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32770, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime, func(context.Context, string) (int, error) { return 200, nil }, func(context.Context, string) error { return nil }))
	result, err := svc.Restart(ctx, "blog", "web", "")
	require.NoError(t, err)
	assert.Equal(t, "c-old", result.Services["web"].After)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

func TestRestart_HTTPPublicationFailureReportsFailedAndRetiresCandidate(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.StopGrace = 10 * time.Millisecond
	spec.Readiness.Timeout = time.Second
	active := restartActive(t, spec)
	rev := restartRevision(spec)
	publishErr := errors.New("publish failed")

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").RunAndReturn(func(context.Context, string) (domain.AppActive, bool, error) {
		return cloneActive(active), true, nil
	})
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	state.EXPECT().LoadRevision(mock.Anything, "blog", "rev-1").Return(rev, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	// ACTIVE is never written: the candidate was not published.
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(publishErr).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", true).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-new").Return(nil).Once()
	var ops []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, op domain.AppOperation) error {
		ops = append(ops, op)
		return nil
	})
	var events []string
	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: &restartTrafficRecorder{events: &events}}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime, func(context.Context, string) (int, error) { return 200, nil }, func(context.Context, string) error { return nil }))

	result, err := svc.Restart(ctx, "blog", "web", "")
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result, "a failed publication is not a deployed restart")
	require.NotEmpty(t, ops)
	last := ops[len(ops)-1]
	assert.Equal(t, domain.AppOutcomeFailed, last.Outcome, "a failed publication must not be journaled as success")
	step, ok := opStep(last, "service.web.restart")
	require.True(t, ok)
	assert.Equal(t, domain.AppStepFailed, step.State, "the restart step must not claim success before publication")
	// The old generation keeps serving and a never-published candidate is
	// removed without a grace period; it is never stopped or served.
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-new", mock.Anything)
	assert.NotContains(t, events, "withdraw")
}

func TestRestart_HTTPPostSavePublicationFailureKeepsPublishedCandidate(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.StopGrace = 10 * time.Millisecond
	spec.Readiness.Timeout = time.Second
	active := restartActive(t, spec)
	rev := restartRevision(spec)
	published := active
	published.Services = map[string]domain.AppEffectiveService{
		"web": {EffectiveRevision: "rev-1", Container: "c-new", Spec: spec},
	}
	loads := 0

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").RunAndReturn(func(context.Context, string) (domain.AppActive, bool, error) {
		loads++
		if loads == 1 {
			return cloneActive(active), true, nil
		}
		return cloneActive(published), true, nil
	})
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	state.EXPECT().LoadRevision(mock.Anything, "blog", "rev-1").Return(rev, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	// ACTIVE is written, then ownership persistence fails: the candidate is
	// the published generation and must keep running.
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(errors.New("ownership failed")).Once()
	var ops []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, op domain.AppOperation) error {
		ops = append(ops, op)
		return nil
	})
	var events []string
	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: &restartTrafficRecorder{events: &events}}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime, func(context.Context, string) (int, error) { return 200, nil }, func(context.Context, string) error { return nil }))

	result, err := svc.Restart(ctx, "blog", "web", "")
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result, "a post-save publication failure is not a deployed restart")
	require.NotEmpty(t, ops)
	assert.Equal(t, domain.AppOutcomeFailed, ops[len(ops)-1].Outcome)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-new", mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
}

func TestRestart_HTTPTrafficFailureReportsFailedAndKeepsPublishedCandidate(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.StopGrace = 10 * time.Millisecond
	spec.Readiness.Timeout = time.Second
	active := restartActive(t, spec)
	rev := restartRevision(spec)

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return(nil, nil).Once()
	state.EXPECT().LoadRevision(mock.Anything, "blog", "rev-1").Return(rev, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return([]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 32771, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.MatchedBy(func(inhibition domain.AppRecoveryInhibition) bool {
		return inhibition.Service == "web" && inhibition.ContainerID == "c-old" && inhibition.Reason == domain.AppInhibitRetirementPending
	})).Return(nil).Once()
	var ops []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, op domain.AppOperation) error {
		ops = append(ops, op)
		return nil
	})
	var events []string
	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: &restartTrafficRecorder{events: &events, failRebuild: true}}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime, func(context.Context, string) (int, error) { return 200, nil }, func(context.Context, string) error { return nil }))

	result, err := svc.Restart(ctx, "blog", "web", "")
	require.ErrorContains(t, err, "traffic rebuild failed")
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result, "a published but unroutable candidate is not a deployed restart")
	require.NotEmpty(t, ops)
	assert.Equal(t, domain.AppOutcomeFailed, ops[len(ops)-1].Outcome, "an unroutable publication must not be journaled as success")
	assert.Equal(t, []string{"traffic", "traffic"}, events, "the graph apply is retried once per restart phase and neither retry may claim success")
	// ACTIVE names the candidate: neither generation may be retired while
	// routing is unproven.
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-new", mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-old", mock.Anything)
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
}
