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

func TestDeploy_HandoverPublishesAfterReady(t *testing.T) {
	state, runtime, images, secrets := mockDeps(t)
	rev := statefulMockRevision()
	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().VolumeExists(mock.Anything, mock.Anything).Return(false, nil)
	runtime.EXPECT().CreateVolume(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	var order []string
	expectSequentialCreate(state, runtime, &order, "c-new", 18080)
	// Tighten the helper's optional policy expectation to record this ordering.
	for _, call := range runtime.ExpectedCalls {
		if call.Method == "UpdateRestartPolicy" {
			call.Run(func(mock.Arguments) { recordEvent(&order, "policy") }).Once()
		}
		if call.Method == "CreateContainer" {
			call.Run(func(args mock.Arguments) {
				assert.Empty(t, args.Get(1).(*domain.ContainerConfig).RestartPolicy)
			})
		}
	}
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).RunAndReturn(func(context.Context, domain.AppRecoveryInhibition) error { recordEvent(&order, "inhibit"); return nil }).Once()
	expectRetireOld(state, runtime, &order, "c-old")
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(func(_ context.Context, active domain.AppActive) error {
		assert.Equal(t, "c-new", active.Services["web"].Container)
		recordEvent(&order, "publish-active")
		return nil
	}).Once()
	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: &orderedTraffic{events: &order}, Handover: deployment.HandoverConfig{MaxHold: time.Minute}}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { recordEvent(&order, "ready"); return 200, nil }, func(context.Context, string) error { return nil }))
	result, err := svc.Deploy(context.Background(), deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, []string{"create-new", "hold", "inhibit", "stop-old", "remove-old", "start-new", "policy", "ready", "publish-active", "traffic"}, order)
}

func TestDeploy_HandoverPublicationFailureReleasesHold(t *testing.T) {
	state, runtime, images, secrets := mockDeps(t)
	rev := statefulMockRevision()
	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().VolumeExists(mock.Anything, mock.Anything).Return(false, nil)
	runtime.EXPECT().CreateVolume(mock.Anything, mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	var order []string
	expectSequentialCreate(state, runtime, &order, "c-new", 18080)
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).Return(nil).Once()
	expectRetireOld(state, runtime, &order, "c-old")
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(func(context.Context, domain.AppActive) error {
		recordEvent(&order, "publish-active")
		return assert.AnError
	}).Once()
	svc := deployment.NewService(deployment.Deps{State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: &orderedTraffic{events: &order}, Handover: deployment.HandoverConfig{MaxHold: time.Minute}}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil }, func(context.Context, string) error { return nil }))
	_, err := svc.Deploy(context.Background(), deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	require.Contains(t, order, "publish-active")
	assert.Equal(t, "withdraw", order[len(order)-1], "the hold is released once publication failed")
}
