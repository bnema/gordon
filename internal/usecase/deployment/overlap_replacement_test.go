package deployment_test

import (
	"context"
	"os"
	"path/filepath"
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

// TestDeploy_NonOverlapEligibleServicesKeepSequentialOrder proves a service
// with a volume, a bind, or a device is never run beside its predecessor: the
// service is withdrawn, the superseded container is stopped and removed, and
// only then is the replacement created.
func TestDeploy_NonOverlapEligibleServicesKeepSequentialOrder(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "binds", "config")
	require.NoError(t, os.MkdirAll(source, 0o755))

	cases := []struct {
		name         string
		mutate       func(*domain.AppService)
		inhibits     bool
		cdiSupported bool
	}{
		{"volume", func(s *domain.AppService) { s.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}} }, true, false},
		{"bind", func(s *domain.AppService) {
			s.Binds = []domain.AppBind{{Name: "config", Path: "/etc/app.conf", ReadOnly: true}}
		}, true, false},
		{"device", func(s *domain.AppService) { s.Devices = []string{"test_gpu"} }, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			state, runtime, images, secrets := mockDeps(t)
			rev := mockRevision()
			tc.mutate(&rev.Spec.Services[0])

			expectDeployPreflight(state, runtime, images, secrets, rev)
			runtime.EXPECT().VolumeExists(mock.Anything, mock.Anything).Return(false, nil).Maybe()
			runtime.EXPECT().CreateVolume(mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			if tc.cdiSupported {
				runtime.EXPECT().SupportsCDIDevices(mock.Anything).Return(nil)
			}
			expectNetworkProvision(runtime, "app-blog", 1)
			state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
			state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
			if tc.inhibits {
				state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).Return(nil).Once()
			}

			var order []string
			expectRetireOld(state, runtime, &order, "c-old")
			expectSequentialCreate(state, runtime, &order, "c-new", 18080)
			state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(
				func(context.Context, domain.AppActive) error {
					recordEvent(&order, "publish-active")
					return nil
				}).Once()
			state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
			state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()

			svc := deployment.NewService(deployment.Deps{
				State: state, Runtime: runtime, Images: images, Secrets: secrets,
				Traffic: &orderedTraffic{events: &order},
			}, zerowrap.Default()).
				WithBindPolicies(map[string]domain.AppBindPolicy{"config": {
					Name: "config", Source: source, Root: root, AllowedApps: []string{"blog"}, AllowedServices: []string{"web"},
				}}).
				WithDevicePolicies(map[string]domain.AppDevicePolicy{"test_gpu": {
					Name: "test_gpu", CDI: []string{"example.com/gpu=GPU-test-uuid"},
					AllowedApps: []string{"blog"}, AllowedServices: []string{"web"},
				}}).
				WithProbeDeps(deployment.NewTestProbeDeps(runtime,
					func(context.Context, string, string) (int, error) { return 200, nil },
					func(context.Context, string) error { return nil },
				))

			result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
			require.NoError(t, err)
			assert.Equal(t, "deployed", result.Services["web"].Result)
			assert.Equal(t,
				[]string{"withdraw", "stop-old", "remove-old", "create-new", "start-new", "publish-active", "traffic"},
				order, "the superseded container is gone before the replacement is created")
		})
	}
}

// TestDeploy_OverlapPublicationFailureKeepsOldContainer proves a failed ACTIVE
// publication of an overlap replacement never stops the superseded container,
// which still serves because ACTIVE and routing were not switched. The
// candidate stays journaled as the step's After, so reconciliation removes it,
// and the failure is reported with a leftover warning on the old container.
func TestDeploy_OverlapPublicationFailureKeepsOldContainer(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	expectSequentialCreate(state, runtime, nil, "c-new", 18080)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).Return(assert.AnError).Once()

	var events []string
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &orderedTraffic{events: &events},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Equal(t, "c-new", result.Services["web"].After)
	require.Len(t, result.Services["web"].CleanupWarnings, 1)
	assert.Equal(t, "c-old", result.Services["web"].CleanupWarnings[0].Leftover)
	assert.Empty(t, events, "the proxy graph was neither withdrawn nor rebuilt")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, mock.Anything, mock.Anything)
}

// seedSwitchedApp seeds an app whose ACTIVE record already names the new
// container c-new, while the superseded c-old was left running by an overlap
// replacement that stopped before retiring it. The caller supplies the journal
// step recording that replacement.
func seedSwitchedApp(t *testing.T, ctx context.Context, store interface {
	SaveIntent(context.Context, domain.AppStopIntent) error
	SaveActive(context.Context, domain.AppActive) error
	SaveOperation(context.Context, domain.AppOperation) error
}, terminal bool, step domain.AppOperationStep) {
	t.Helper()
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog", Stopped: true}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-new", EffectiveRevision: "rev-1", Spec: headlessSpec(), BackendBinds: map[int]int{8080: 18081}},
	}}))
	op := domain.AppOperation{
		Op: "op-overlap", Kind: "deploy", App: "blog", StartedAt: time.Now().UTC(),
		Steps: []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepSucceeded}, step},
	}
	if terminal {
		op.Outcome = domain.AppOutcomeSuccess
	}
	require.NoError(t, store.SaveOperation(ctx, op))
}

// TestBootRecovery_RetiresSupersededContainerAfterInterruptedSwitch proves the
// crash window after the switch: ACTIVE names the new container, the old one
// is still running and unpublished, so reconciliation republishes routing and
// then stops it gracefully with the default grace.
func TestBootRecovery_RetiresSupersededContainerAfterInterruptedSwitch(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedSwitchedApp(t, ctx, store, false, domain.AppOperationStep{
		ID: "service.web.replace", State: domain.AppStepPending, Service: "web",
		Before: "c-old", After: "c-new", Overlap: true,
	})

	var order []string
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", domain.AppDefaultStopGrace).RunAndReturn(
		func(context.Context, string, time.Duration) error {
			recordEvent(&order, "stop-old")
			return nil
		}).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).RunAndReturn(
		func(context.Context, string, bool) error {
			recordEvent(&order, "remove-old")
			return nil
		}).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").Return(&domain.Container{ID: "c-new", Status: "exited"}, nil)

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &orderedTraffic{events: &order},
	}, zerowrap.Default())
	require.NoError(t, svc.ReconcileBoot(ctx))

	assert.Equal(t, []string{"traffic", "stop-old", "remove-old"}, order,
		"routing is republished to the published container before the superseded one is stopped")
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-new", mock.Anything)

	op, err := store.LoadOperation(ctx, "blog", "op-overlap")
	require.NoError(t, err)
	require.True(t, op.Terminal())
	step, found := opStep(op, "service.web.replace")
	require.True(t, found)
	assert.True(t, step.BeforeRetired)
	assert.Empty(t, step.After, "a converged step is not retried")
	assert.Contains(t, step.Detail, "retired superseded container c-old")
	active, _, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	assert.Equal(t, "c-new", active.Services["web"].Container)

	// A converged step is not retried on the next pass.
	require.NoError(t, svc.ReconcileBoot(ctx))
	runtime.AssertNumberOfCalls(t, "StopContainer", 1)
}

// TestReconcile_RetriesSupersededContainerRetirementOfASucceededDeploy proves a
// deploy that succeeded with a cleanup warning (the old container could not be
// removed) is retried by the next reconciliation, and that a failure to retire
// is a warning that does not block the app.
func TestReconcile_RetriesSupersededContainerRetirementOfASucceededDeploy(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedSwitchedApp(t, ctx, store, true, domain.AppOperationStep{
		ID: "service.web.replace", State: domain.AppStepSucceeded, Service: "web",
		Before: "c-old", After: "c-new", Overlap: true,
	})

	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-new").Return(&domain.Container{ID: "c-new", Status: "exited"}, nil)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(assert.AnError).Once()
	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &orderedTraffic{},
	}, zerowrap.Default())

	require.NoError(t, svc.ReconcileBoot(ctx), "a leftover old container does not block reconciliation")
	op, err := store.LoadOperation(ctx, "blog", "op-overlap")
	require.NoError(t, err)
	step, _ := opStep(op, "service.web.replace")
	assert.False(t, step.BeforeRetired, "the retirement failed and stays pending")
	assert.Equal(t, domain.AppStepSucceeded, step.State, "the deploy outcome is untouched")
	assert.Equal(t, domain.AppOutcomeSuccess, op.Outcome)
	require.Len(t, op.Warnings, 1)
	assert.Equal(t, "c-old", op.Warnings[0].Leftover)

	// The next pass retries, and now succeeds.
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	require.NoError(t, svc.ReconcileBoot(ctx))
	op, err = store.LoadOperation(ctx, "blog", "op-overlap")
	require.NoError(t, err)
	step, _ = opStep(op, "service.web.replace")
	assert.True(t, step.BeforeRetired)
}

// TestReconcile_NeverRetiresASupersededContainerThatActiveNamesAgain proves
// reconciliation does not stop a container ACTIVE currently publishes, such
// as when a rollback re-published the previous generation.
func TestReconcile_NeverRetiresASupersededContainerThatActiveNamesAgain(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog", Stopped: true}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-old", EffectiveRevision: "rev-0", Spec: headlessSpec()},
	}}))
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-overlap", Kind: "deploy", App: "blog", StartedAt: time.Now().UTC(), Outcome: domain.AppOutcomeFailed,
		Steps: []domain.AppOperationStep{{
			ID: "service.web.replace", State: domain.AppStepFailed, Service: "web",
			Before: "c-old", After: "c-new", Overlap: true,
		}},
	}))

	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", true).Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").Return(&domain.Container{ID: "c-old", Status: "exited"}, nil)
	svc := deployment.NewService(deployment.Deps{State: store, Runtime: runtime, Images: images, Secrets: secrets}, zerowrap.Default())
	require.NoError(t, svc.ReconcileBoot(ctx))

	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-old", mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-old", mock.Anything)
}
