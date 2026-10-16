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

// opStep returns one journaled step by id.
func opStep(op domain.AppOperation, id string) (domain.AppOperationStep, bool) {
	for _, step := range op.Steps {
		if step.ID == id {
			return step, true
		}
	}
	return domain.AppOperationStep{}, false
}

// TestDeploy_TrafficFailureRecordsFailedServiceStep proves the ordering
// invariant: a service step is never journaled as succeeded while the
// routing graph refused its publication.
func TestDeploy_TrafficFailureRecordsFailedServiceStep(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()
	rev.Spec.Services[0].StopGrace = time.Millisecond

	oldActive := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old"},
	}}

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return(
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil)
	runtime.EXPECT().InspectImageVolumes(mock.Anything, rev.Spec.Services[0].Image).Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(oldActive, true, nil)
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(nil)

	expectNetworkProvision(runtime, "app-blog", 1)
	// The superseded container serves beside the candidate and must survive
	// the failed switch: no stop, no removal, no claim release.
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18080, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	// The durable retirement marker is the cleanup owner of the superseded
	// container; it is recorded before the switch and never cleared here.
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.MatchedBy(func(i domain.AppRecoveryInhibition) bool {
		return i.ContainerID == "c-old" && i.Reason == domain.AppInhibitRetirementPending
	})).Return(nil).Once()

	var saved []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, op domain.AppOperation) error {
			saved = append(saved, op)
			return nil
		})

	traffic := &recordingTraffic{failRebuild: true}
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result, "a service without routable publication is not deployed")

	require.NotEmpty(t, saved)
	last := saved[len(saved)-1]
	assert.Equal(t, domain.AppOutcomeFailed, last.Outcome, "a failed publication must not be journaled as success")
	step, ok := opStep(last, "service.web.replace")
	require.True(t, ok)
	assert.Equal(t, domain.AppStepFailed, step.State, "the service step must not claim success before traffic applied")
	// ACTIVE already names c-new while the proxy graph was refused, so the
	// superseded container may still be the one a stale graph reaches: it is
	// kept running. Both generations stay traceable in the journal for
	// reconciliation, and the leftover is reported.
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, mock.Anything, mock.Anything)
	assert.Equal(t, "c-old", step.Before)
	assert.Equal(t, "c-new", step.After)
	require.Len(t, result.Services["web"].CleanupWarnings, 1)
	assert.Equal(t, "c-old", result.Services["web"].CleanupWarnings[0].Leftover)
	require.Len(t, last.Warnings, 1)
	assert.Equal(t, "c-old", last.Warnings[0].Leftover)
}

// TestStop_TrafficFailureRecordsTerminalFailure proves a rejected graph
// apply after a stop is recorded as a failure, not as a successful stop
// whose rerun would read success.
func TestStop_TrafficFailureRecordsTerminalFailure(t *testing.T) {
	ctx := context.Background()
	state, runtime, _, _ := mockDeps(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-1").Return(nil).Once()

	var saved []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, op domain.AppOperation) error {
			saved = append(saved, op)
			return nil
		})

	traffic := &recordingTraffic{failRebuild: true}
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Traffic: traffic,
	}, zerowrap.Default())

	result, err := svc.Stop(ctx, "blog", "")
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result, "the workload really was stopped")

	require.NotEmpty(t, saved)
	terminal := saved[len(saved)-1]
	assert.Equal(t, domain.AppOutcomeFailed, terminal.Outcome, "the rejected graph apply must not replay as success")
	step, ok := opStep(terminal, "traffic.publish")
	require.True(t, ok, "the rejected publication is journaled")
	assert.Equal(t, domain.AppStepFailed, step.State)
}
