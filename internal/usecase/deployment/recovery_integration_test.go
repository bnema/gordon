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

// newTestStore opens a real app-state store in a temporary directory.
func newTestStore(t *testing.T) *appstate.Store {
	t.Helper()
	store, err := appstate.NewStore(t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

// TestRecoveryInhibition_RefusesInhibitedGenerationAfterReboot proves the
// durable inhibition contract end to end: an old volume-owning container
// that a replacement may already have superseded is never started again,
// even by a completely reconstructed engine reading a reopened store.
// Unrelated generations of the same app stay recoverable.
func TestRecoveryInhibition_RefusesInhibitedGenerationAfterReboot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	store, err := appstate.NewStore(dir, zerowrap.Default())
	require.NoError(t, err)
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				Container: "c-old", EffectiveRevision: "rev-1", Spec: headlessSpec(),
				BackendBinds: map[int]int{8080: 32770},
			},
			"worker": {
				Container: "c-worker", EffectiveRevision: "rev-1", Spec: headlessSpec(),
			},
		},
	}))
	require.NoError(t, store.SaveRecoveryInhibition(ctx, domain.AppRecoveryInhibition{
		App: "blog", Service: "web", ContainerID: "c-old",
		Reason: domain.AppInhibitReplacementPending, Operation: "op-1",
	}))
	require.NoError(t, store.Close())

	// Reboot: a fresh store handle and a fresh engine, no in-memory state.
	reopened, err := appstate.NewStore(dir, zerowrap.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	inhibitions, err := reopened.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, inhibitions, 1)
	assert.Equal(t, "c-old", inhibitions[0].ContainerID)

	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-worker").
		Return(&domain.Container{ID: "c-worker", Status: "running", StartedAt: time.Now().UTC()}, nil).Twice()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "c-worker").Return("", false, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: reopened, Runtime: runtime, Traffic: &recordingTraffic{},
	}, zerowrap.Default())

	err = svc.ReconcileRunning(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recovery inhibited")
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, "c-old")
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, "c-old", mock.Anything)
	runtime.AssertCalled(t, "InspectContainer", mock.Anything, "c-worker")
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveVolume", mock.Anything, mock.Anything, mock.Anything)
}

// seedDesired writes one accepted desired revision through the real
// staged/committed/materialized apply protocol.
func seedDesired(t *testing.T, ctx context.Context, store *appstate.Store, rev domain.AppDesiredRevision) {
	t.Helper()
	intentID := "intent-1"
	require.NoError(t, store.StageApply(ctx, domain.AppApplyIntent{
		Intent: intentID, App: rev.App, Revision: rev.Revision,
		SourceSHA256: "0f1e2d", Spec: rev.Spec, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, store.CommitApply(ctx, rev.App, intentID))
	require.NoError(t, store.MaterializeApply(ctx, rev.App, intentID))
}

// TestInterruptedVolumeFailureLeavesOldGenerationInhibited proves the
// marker is written BEFORE a volume-owning replacement can write and is
// not cleared when that replacement fails: the old generation must never
// be restarted on top of possibly-newer data.
func TestInterruptedVolumeFailureLeavesOldGenerationInhibited(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)

	spec := domain.AppService{
		Name:    "web",
		Image:   "registry.example.com/blog/web:1.4.2",
		Volumes: []domain.AppVolume{{Name: "data", Path: "/data"}},
		Secrets: map[string]string{},
		Readiness: domain.AppReadiness{
			Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: 50 * time.Millisecond,
		},
	}
	rev := testRevision("blog", spec)
	rev.Spec.Env = map[string]string{}

	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				Container: "c-old", EffectiveRevision: "rev-0", Spec: headlessSpec(),
				BackendBinds: map[int]int{9000: 19000},
			},
		},
	}))
	seedDesired(t, ctx, store, rev)

	images.EXPECT().ResolveDigest(mock.Anything, spec.Image).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, spec.Image).Return(nil, nil).Once()
	runtime.EXPECT().VolumeExists(mock.Anything, mock.Anything).Return(false, nil).Once()
	// The superseded writer cannot be removed: the replacement must abort
	// rather than risk overlapping writers, and the inhibition written
	// before the stop attempt stays in place.
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(assert.AnError).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
		ImagePolicy: domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 500, nil },
		func(context.Context, string) error { return assert.AnError },
	))

	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)

	inhibitions, err := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, err)
	require.Len(t, inhibitions, 1)
	assert.Equal(t, "c-old", inhibitions[0].ContainerID)
	assert.Equal(t, domain.AppInhibitReplacementPending, inhibitions[0].Reason)

	// A later boot must refuse to revive the inhibited generation, and a
	// rebuilt engine must reach the same conclusion.
	runtime2 := outmocks.NewMockContainerRuntime(t)
	rebooted := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime2, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	bootErr := rebooted.ReconcileBoot(ctx)
	require.Error(t, bootErr)
	assert.Contains(t, bootErr.Error(), "recovery inhibited")
	runtime2.AssertNotCalled(t, "StartContainer", mock.Anything, "c-old")
	runtime2.AssertNotCalled(t, "RestartContainer", mock.Anything, "c-old", mock.Anything)
	runtime2.AssertNotCalled(t, "RemoveVolume", mock.Anything, mock.Anything, mock.Anything)
}

// TestRemove_InhibitsBeforeRuntimeWithdrawal proves remove is durably
// fail-closed: stopped intent and an inhibition per removed container are
// persisted before any runtime effect, and volumes are never deleted.
func TestRemove_InhibitsBeforeRuntimeWithdrawal(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()

	var order []string
	state.EXPECT().SaveIntent(mock.Anything, mock.MatchedBy(func(intent domain.AppStopIntent) bool {
		return intent.Stopped
	})).RunAndReturn(func(context.Context, domain.AppStopIntent) error {
		order = append(order, "intent-stopped")
		return nil
	}).Once()
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.MatchedBy(func(inhibition domain.AppRecoveryInhibition) bool {
		return inhibition.App == "blog" && inhibition.Service == "web" && inhibition.ContainerID == "c-1"
	})).RunAndReturn(func(context.Context, domain.AppRecoveryInhibition) error {
		order = append(order, "inhibited")
		return nil
	}).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", mock.Anything).RunAndReturn(func(context.Context, string, time.Duration) error {
		order = append(order, "stopped")
		return nil
	}).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).RunAndReturn(func(context.Context, string, bool) error {
		order = append(order, "removed")
		return nil
	}).Once()
	// Confirmed disappearance releases the container's backend claims and
	// clears its recovery inhibition before the incarnation is retired.
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-1").Return(nil).Once()
	// Ownership is read while it is still live; the name-only record owns
	// no network, so nothing is reclaimed.
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog"}, nil).Once()
	// The incarnation is retired atomically after the workload is gone.
	state.EXPECT().RetireApp(mock.Anything, "blog").RunAndReturn(func(context.Context, string) error {
		order = append(order, "retired")
		return nil
	}).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	result, err := svc.Remove(ctx, "blog", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{"intent-stopped", "inhibited", "stopped", "removed", "retired"}, order)
	runtime.AssertNotCalled(t, "RemoveVolume", mock.Anything, mock.Anything, mock.Anything)
}

// TestBootStoppedConvergenceStopsRevivedContainer proves durable stopped
// intent wins over native restart policy: a container revived while the
// daemon was away is stopped again, without removal, and is never started.
func TestBootStoppedConvergenceStopsRevivedContainer(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").
		Return(domain.AppStopIntent{App: "blog", Stopped: true}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-1").
		Return(&domain.Container{ID: "c-1", Status: "running"}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", mock.Anything).Return(nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	require.NoError(t, svc.ReconcileBoot(ctx))
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

// TestBootRefusesInhibitedGenerationBeforeAnyRuntimeEffect proves boot
// recovery checks the durable marker before touching the workload.
func TestBootRefusesInhibitedGenerationBeforeAnyRuntimeEffect(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-old", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "blog").Return(domain.AppStopIntent{App: "blog"}, nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").
		Return([]domain.AppRecoveryInhibition{{App: "blog", Service: "web", ContainerID: "c-old"}}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	err := svc.ReconcileBoot(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "recovery inhibited")
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
}

// TestWorkloadMutationsSerializePerApp proves a periodic pass cannot
// interleave with an in-flight user mutation of the same app: the pass
// skips the busy app instead of waiting for it.
func TestWorkloadMutationsSerializePerApp(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	state.EXPECT().Recover(mock.Anything).RunAndReturn(func(context.Context) error {
		close(entered)
		<-release
		return assert.AnError
	}).Once()
	// Only the app list may be read: the pass must not touch intent or
	// ACTIVE while the mutation holds the app lock.
	state.EXPECT().ListApps(mock.Anything).Return([]string{"blog"}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())

	done := make(chan error, 1)
	go func() {
		_, err := svc.Stop(ctx, "blog", "")
		done <- err
	}()
	<-entered

	// The mutation holds the app lock. The periodic pass must skip the
	// app instead of waiting: LoadIntent has no expectation, so reaching
	// it would fail the test.
	require.NoError(t, svc.ReconcileRunning(ctx))
	state.AssertNotCalled(t, "LoadIntent", mock.Anything, mock.Anything)

	close(release)
	require.Error(t, <-done)
}

// TestRestart_RefusesInhibitedGeneration proves an explicit operator
// restart cannot revive a generation whose recovery is durably inhibited:
// a replacement may already have written to the volume it still owns.
func TestRestart_RefusesInhibitedGeneration(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-old", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().LoadRecoveryInhibitions(mock.Anything, "blog").Return([]domain.AppRecoveryInhibition{{
		App: "blog", Service: "web", ContainerID: "c-old", Reason: domain.AppInhibitReplacementPending,
	}}, nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	result, err := svc.Restart(ctx, "blog", "", "")
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Contains(t, result.Services["web"].Error, "recovery inhibited")
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, mock.Anything, mock.Anything)
}

// TestRemove_KeepsStateWhenAContainerCannotBeRemoved proves remove never
// loses track of a survivor: ACTIVE, stopped intent, and the inhibition
// stay in place so the periodic pass keeps converging it.
func TestRemove_KeepsStateWhenAContainerCannotBeRemoved(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().SaveIntent(mock.Anything, mock.MatchedBy(func(intent domain.AppStopIntent) bool {
		return intent.Stopped
	})).Return(nil).Once()
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).Return(nil).Once()
	// The claim journal is written before the runtime effects and is
	// rewritten terminally when the removal cannot complete: a repeat of
	// the same key replays the failure instead of reading a stuck claim.
	state.EXPECT().SaveOperation(mock.Anything, mock.MatchedBy(func(op domain.AppOperation) bool {
		return op.Op != "" && op.Kind == "remove" && !op.Terminal()
	})).Return(nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.MatchedBy(func(op domain.AppOperation) bool {
		return op.Op != "" && op.Outcome == domain.AppOutcomeFailed && op.Terminal()
	})).Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", mock.Anything).Return(assert.AnError).Once()

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	_, err := svc.Remove(ctx, "blog", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not confirmed gone")
	// ACTIVE and the stopped intent were never discarded: the monitor can
	// still converge the survivor. The survivor's claims and inhibition
	// stay in place too: only a confirmed disappearance releases them.
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "RetireApp", mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ClearRecoveryInhibition", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveVolume", mock.Anything, mock.Anything, mock.Anything)
}

// TestRemovalOfMissingContainerIsIdempotent proves a container the
// runtime already dropped is a successful removal, not a failure.
func TestRemovalOfMissingContainerIsIdempotent(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	active := domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-1", EffectiveRevision: "rev-1", Spec: headlessSpec()},
	}}
	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(active, true, nil).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveIntent(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().SaveRecoveryInhibition(mock.Anything, mock.Anything).Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-1", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-1", false).Return(domain.ErrContainerNotFound).Once()
	// Not-found is confirmation: the stale claims and the inhibition of
	// the disappeared container are released.
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-1").Return(nil).Once()
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-1").Return(nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog"}, nil).Once()
	state.EXPECT().RetireApp(mock.Anything, "blog").Return(nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	result, err := svc.Remove(ctx, "blog", "")
	require.NoError(t, err)
	require.NotNil(t, result)
}
