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

// seedRevision materializes one revision of an app, so a later recovery can
// rebuild that generation from its pinned digest.
func seedRevision(t *testing.T, ctx context.Context, store *appstate.Store, intentID, supersedes string, rev domain.AppDesiredRevision) {
	t.Helper()
	require.NoError(t, store.StageApply(ctx, domain.AppApplyIntent{
		Intent: intentID, App: rev.App, Revision: rev.Revision, Supersedes: supersedes,
		SourceSHA256: "0f1e2d", Spec: rev.Spec, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, store.CommitApply(ctx, rev.App, intentID))
	require.NoError(t, store.MaterializeApply(ctx, rev.App, intentID))
}

// seedReplaceableApp seeds one app whose ACTIVE generation (rev-0) has a
// materialized superseding revision (rev-1). The published container c-old is
// gone from the runtime: the caller decides what the runtime reports for it.
func seedReplaceableApp(t *testing.T, ctx context.Context, store *appstate.Store) (domain.AppDesiredRevision, domain.AppDesiredRevision) {
	t.Helper()
	spec := webService()
	spec.Secrets = map[string]string{}
	spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: time.Second}
	activeRev := testRevision("blog", spec)
	activeRev.Revision = "rev-0"
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {
			Container: "c-old", EffectiveRevision: "rev-0", Image: spec.Image,
			Digest: restartTestDigest, Spec: spec, BackendBinds: map[int]int{8080: 18080},
		},
	}}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(t, ctx, store, "intent-0", "", activeRev)
	desired := testRevision("blog", spec)
	seedRevision(t, ctx, store, "intent-1", "rev-0", desired)
	return activeRev, desired
}

// TestDeploy_CandidateIsJournaledBeforeItStarts proves the crash window is
// closed as far as a container ID allows: the candidate ID reaches the
// durable journal before the container is started, and the journal is
// finalized as a success only after publication.
func TestDeploy_CandidateIsJournaledBeforeItStarts(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedReplaceableApp(t, ctx, store)

	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/web:1.4.2").Return(restartTestDigest, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/web:1.4.2").Return(nil, nil).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-new", Name: "web"}, nil).Once()

	journaledBeforeStart := false
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").RunAndReturn(func(context.Context, string) error {
		op, ok, err := store.LoadLatestOperation(context.Background(), "blog")
		require.NoError(t, err)
		require.True(t, ok, "the replacement is journaled before it starts")
		require.False(t, op.Terminal(), "the journal is still in flight while the candidate starts")
		step, found := opStep(op, "service.web.replace")
		journaledBeforeStart = found && step.After == "c-new" && step.State == domain.AppStepPending
		return nil
	}).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "op-journal"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.True(t, journaledBeforeStart,
		"the candidate ID is durable before the container is started")

	op, err := store.LoadOperation(context.Background(), "blog", "op-journal")
	require.NoError(t, err)
	assert.Equal(t, domain.AppOutcomeSuccess, op.Outcome)
	step, found := opStep(op, "service.web.replace")
	require.True(t, found)
	assert.Equal(t, domain.AppStepSucceeded, step.State)
	assert.Equal(t, "c-new", step.After)
}

// crashDuringReplacement runs one replacement deploy and interrupts it
// between creating the candidate and publishing it. The candidate is left
// running, ACTIVE still names the superseded generation, and the operation
// journal stays in flight: the state a process crash or a failed cleanup
// leaves behind.
func crashDuringReplacement(t *testing.T, store *appstate.Store, opKey string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)

	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/web:1.4.2").Return(restartTestDigest, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/web:1.4.2").Return(nil, nil).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-orphan", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-orphan").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-orphan", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	// The canceled context also refuses the candidate cleanup, so the
	// candidate outlives the operation exactly as a crash would leave it.
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-orphan", true).RunAndReturn(
		func(removeCtx context.Context, _ string, _ bool) error {
			return removeCtx.Err()
		}).Once()

	probeStarted := make(chan struct{})
	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(probeCtx context.Context, _ string) (int, error) {
			select {
			case <-probeStarted:
			default:
				close(probeStarted)
			}
			<-probeCtx.Done()
			return 0, probeCtx.Err()
		},
		func(context.Context, string) error { return nil },
	))
	done := make(chan error, 1)
	go func() {
		_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: opKey})
		done <- err
	}()
	select {
	case <-probeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the readiness probe")
	}
	cancel()
	require.Error(t, <-done)
}

// TestBootRecovery_RemovesUnpublishedCandidateBeforeRebuildingPublished
// proves the crash window between creating a replacement and publishing it is
// recoverable: an interruption leaves the candidate journaled but unpublished,
// and boot recovery removes that candidate before it rebuilds the generation
// recorded in ACTIVE, so two generations of one service never run together.
func TestBootRecovery_RemovesUnpublishedCandidateBeforeRebuildingPublished(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedReplaceableApp(t, ctx, store)
	crashDuringReplacement(t, store, "op-crash")

	// The crash left the candidate durable but unpublished, and ACTIVE still
	// names the superseded generation.
	interrupted, err := store.LoadOperation(context.Background(), "blog", "op-crash")
	require.NoError(t, err)
	assert.False(t, interrupted.Terminal(), "an interrupted operation stays in flight until recovery finalizes it")
	crashStep, found := opStep(interrupted, "service.web.replace")
	require.True(t, found)
	assert.Equal(t, domain.AppStepPending, crashStep.State)
	assert.Equal(t, "c-orphan", crashStep.After, "the created candidate is durably recorded")
	active, ok, err := store.LoadActive(context.Background(), "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "c-old", active.Services["web"].Container)

	// Boot recovery: the orphan is removed before the published generation is
	// rebuilt from its pinned revision.
	var order []string
	runtime2 := outmocks.NewMockContainerRuntime(t)
	runtime2.EXPECT().RemoveContainer(mock.Anything, "c-orphan", true).RunAndReturn(
		func(context.Context, string, bool) error {
			order = append(order, "remove-orphan")
			return nil
		}).Once()
	runtime2.EXPECT().IsContainerRunning(mock.Anything, "c-old").Return(false, nil).Once()
	runtime2.EXPECT().StartContainer(mock.Anything, "c-old").Return(domain.ErrContainerNotFound).Once()
	runtime2.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime2.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(domain.ErrContainerNotFound).Once()
	expectNetworkProvision(runtime2, "app-blog", 1)
	runtime2.EXPECT().CreateContainer(mock.Anything, mock.Anything).RunAndReturn(
		func(context.Context, *domain.ContainerConfig) (*domain.Container, error) {
			order = append(order, "rebuild-published-generation")
			return &domain.Container{ID: "c-rebuilt", Name: "web"}, nil
		}).Once()
	runtime2.EXPECT().StartContainer(mock.Anything, "c-rebuilt").Return(nil).Once()
	runtime2.EXPECT().GetContainerBackendBinds(mock.Anything, "c-rebuilt", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18082, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	rebooted := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime2, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime2,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))
	// Boot recovery runs on a fresh process context: the crashed deploy's
	// context is gone with it.
	require.NoError(t, rebooted.ReconcileBoot(ctx))

	assert.Equal(t, []string{"remove-orphan", "rebuild-published-generation"}, order,
		"the unpublished candidate is gone before the published generation is rebuilt")
	runtime2.AssertNotCalled(t, "StartContainer", mock.Anything, "c-orphan")
	runtime2.AssertNotCalled(t, "RestartContainer", mock.Anything, "c-orphan", mock.Anything)

	// The interrupted operation is finalized as failed, so a repeat of its key
	// replays an explicit failure instead of a permanent in-flight claim.
	finalized, err := store.LoadOperation(context.Background(), "blog", "op-crash")
	require.NoError(t, err)
	require.True(t, finalized.Terminal(), "boot recovery finalizes the interrupted operation")
	assert.Equal(t, domain.AppOutcomeFailed, finalized.Outcome)
	finalStep, found := opStep(finalized, "service.web.replace")
	require.True(t, found)
	assert.Equal(t, domain.AppStepFailed, finalStep.State)
	assert.Contains(t, finalStep.Error, "unpublished candidate was removed")

	// ACTIVE now names the rebuilt generation: recovery converged the app
	// instead of leaving it pointing at a container that no longer exists.
	converged, ok, err := store.LoadActive(context.Background(), "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "c-rebuilt", converged.Services["web"].Container)
	assert.Equal(t, "rev-0", converged.Services["web"].EffectiveRevision)
}

// TestDeploy_ReconcilesInterruptedPredecessorBeforeCreatingAReplacement
// proves the live case: when a cleanup failed and Gordon kept running, the
// next mutation of the app removes the leftover candidate before it creates a
// new generation, so two generations of one service never run together even
// without a restart. It also finalizes the interrupted journal.
func TestDeploy_ReconcilesInterruptedPredecessorBeforeCreatingAReplacement(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedReplaceableApp(t, ctx, store)
	crashDuringReplacement(t, store, "op-crash")

	var order []string
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-orphan", true).RunAndReturn(
		func(context.Context, string, bool) error {
			order = append(order, "remove-leftover-candidate")
			return nil
		}).Once()
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/web:1.4.2").Return(restartTestDigest, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/web:1.4.2").Return(nil, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(domain.ErrContainerNotFound).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).RunAndReturn(
		func(context.Context, *domain.ContainerConfig) (*domain.Container, error) {
			order = append(order, "create-new-generation")
			return &domain.Container{ID: "c-next", Name: "web"}, nil
		}).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-next").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-next", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18082, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog", Op: "op-next"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, []string{"remove-leftover-candidate", "create-new-generation"}, order)

	active, ok, err := store.LoadActive(ctx, "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "c-next", active.Services["web"].Container)

	interrupted, err := store.LoadOperation(ctx, "blog", "op-crash")
	require.NoError(t, err)
	require.True(t, interrupted.Terminal(), "the interrupted operation is finalized")
	assert.Equal(t, domain.AppOutcomeFailed, interrupted.Outcome)
	step, found := opStep(interrupted, "service.web.replace")
	require.True(t, found)
	assert.Equal(t, domain.AppStepFailed, step.State)
}

// TestBootRecovery_RemovesCandidateOfAnInterruptedFirstDeployment proves the
// same recovery works when the interrupted operation is the app's FIRST
// deployment: there is no ACTIVE generation to converge, and the leftover
// candidate is still removed and the journal finalized.
func TestBootRecovery_RemovesCandidateOfAnInterruptedFirstDeployment(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-seeded", Kind: "deploy", App: "blog", StartedAt: time.Now().UTC(),
		Steps: []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{ID: "service.web.replace", State: domain.AppStepPending, Service: "web", Before: "c-old", After: "c-orphan"},
		},
	}))

	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-orphan", true).Return(nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	require.NoError(t, svc.ReconcileBoot(ctx))

	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, "c-orphan")

	finalized, err := store.LoadOperation(ctx, "blog", "op-seeded")
	require.NoError(t, err)
	require.True(t, finalized.Terminal())
	assert.Equal(t, domain.AppOutcomeFailed, finalized.Outcome)
	step, found := opStep(finalized, "service.web.replace")
	require.True(t, found)
	assert.Equal(t, domain.AppStepFailed, step.State)
	assert.Contains(t, step.Error, "unpublished candidate was removed")
}

// TestRestart_FailedRebuildClearsTheInhibitionOfTheMissingContainer proves a
// restart that falls back to a rebuild does not leave a durable inhibition for
// a container that is already gone: nothing could ever revive it, and the
// marker would refuse every later start, recovery pass, and restart.
func TestRestart_FailedRebuildClearsTheInhibitionOfTheMissingContainer(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	runtime := outmocks.NewMockContainerRuntime(t)

	spec := webService()
	spec.Secrets = map[string]string{}
	spec.Volumes = []domain.AppVolume{{Name: "data", Path: "/data"}}
	spec.Readiness = domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: 50 * time.Millisecond}
	rev := testRevision("blog", spec)
	rev.Revision = "rev-0"
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {
			Container: "c-old", EffectiveRevision: "rev-0", Image: spec.Image,
			Digest: restartTestDigest, Spec: spec, BackendBinds: map[int]int{8080: 18080},
		},
	}}))
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	seedRevision(t, ctx, store, "intent-0", "", rev)

	runtime.EXPECT().RestartContainer(mock.Anything, "c-old", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(domain.ErrContainerNotFound).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateVolume(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-rebuild", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-rebuild").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-rebuild", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	runtime.EXPECT().GetContainerLogs(mock.Anything, "c-rebuild", false).Return(nil, assert.AnError).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-rebuild", true).Return(nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 503, nil },
		func(context.Context, string) error { return assert.AnError },
	))

	result, err := svc.Restart(ctx, "blog", "web", "op-restart-failed")
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)

	inhibitions, loadErr := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, loadErr)
	assert.Empty(t, inhibitions,
		"the inhibition of a container proven gone must not survive a failed rebuild")
}

// TestBootRecovery_RetriesLeftoverOfATerminalFailedOperation proves a
// leftover candidate recorded by an operation that already reached a terminal
// failure is still converged: the journal keeps the candidate ID, and the next
// recovery pass removes it.
func TestBootRecovery_RetriesLeftoverOfATerminalFailedOperation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog", Stopped: true}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-old", EffectiveRevision: "rev-0", Spec: headlessSpec()},
	}}))
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-failed", Kind: "deploy", App: "blog", StartedAt: time.Now().UTC(),
		Outcome: domain.AppOutcomeFailed,
		Steps: []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{ID: "service.web.replace", State: domain.AppStepFailed, Service: "web", Before: "c-old", After: "c-orphan", Error: "readiness"},
		},
	}))

	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-orphan", true).Return(nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "c-old").Return(&domain.Container{ID: "c-old", Status: "running"}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	require.NoError(t, svc.ReconcileBoot(ctx))

	runtime.AssertCalled(t, "RemoveContainer", mock.Anything, "c-orphan", true)
}

// TestBootRecovery_InterruptionAfterTheLastStepIsASuccess proves an operation
// whose effects all ran but whose final outcome write was interrupted is
// classified as the success it was, and the published generation is kept.
func TestBootRecovery_InterruptionAfterTheLastStepIsASuccess(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog", Stopped: true}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {Container: "c-published", EffectiveRevision: "rev-0", Spec: headlessSpec()},
	}}))
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-unfinished", Kind: "deploy", App: "blog", StartedAt: time.Now().UTC(),
		Steps: []domain.AppOperationStep{
			{ID: "preflight", State: domain.AppStepSucceeded},
			{ID: "service.web.replace", State: domain.AppStepSucceeded, Service: "web", Before: "c-old", After: "c-published"},
		},
	}))

	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, "c-published").Return(&domain.Container{ID: "c-published", Status: "exited"}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	require.NoError(t, svc.ReconcileBoot(ctx))

	reconciled, err := store.LoadOperation(ctx, "blog", "op-unfinished")
	require.NoError(t, err)
	require.True(t, reconciled.Terminal())
	assert.Equal(t, domain.AppOutcomeSuccess, reconciled.Outcome)
	step, found := opStep(reconciled, "service.web.replace")
	require.True(t, found)
	assert.Equal(t, domain.AppStepSucceeded, step.State, "a step that already succeeded is left as recorded")
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-published", mock.Anything)
}

// TestPreflight_RefusesWhileAnOperationIsUnfinished proves the claim guard:
// preflight never masks an unfinished operation of the same app, because that
// would leave its leftover generation untracked forever.
func TestPreflight_RefusesWhileAnOperationIsUnfinished(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	spec := webService()
	spec.Secrets = map[string]string{}
	seedRevision(t, ctx, store, "intent-0", "", testRevision("blog", spec))
	require.NoError(t, store.SaveOperation(ctx, domain.AppOperation{
		Op: "op-unfinished", Kind: "deploy", App: "blog", StartedAt: time.Now().UTC(),
		Steps: []domain.AppOperationStep{{ID: "preflight", State: domain.AppStepSucceeded}},
	}))

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: outmocks.NewMockContainerRuntime(t), Images: images, Secrets: secrets,
	}, zerowrap.Default())
	_, _, err := svc.Preflight(ctx, deployment.DeployInput{App: "blog", Op: "op-other"})
	require.ErrorIs(t, err, domain.ErrAppStateConflict)
	assert.Contains(t, err.Error(), "unfinished operation")
}

// TestRestart_RebuildsMissingActiveContainer proves restart is not a dead end
// when the recorded container is gone: the service is rebuilt from the pinned
// ACTIVE digest and published again.
func TestRestart_RebuildsMissingActiveContainer(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)
	seedReplaceableApp(t, ctx, store)

	runtime.EXPECT().RestartContainer(mock.Anything, "c-old", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).Return(domain.ErrContainerNotFound).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).Return(domain.ErrContainerNotFound).Once()
	expectNetworkProvision(runtime, "app-blog", 1)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-rebuilt", Name: "web"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-rebuilt").Return(nil).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-rebuilt", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18082, Protocol: domain.NetworkProtocolTCP}}, nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 200, nil },
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Restart(ctx, "blog", "web", "op-restart-missing")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t, "c-rebuilt", result.Services["web"].After,
		"the missing generation is rebuilt from the pinned ACTIVE digest")

	active, ok, err := store.LoadActive(context.Background(), "blog")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "c-rebuilt", active.Services["web"].Container)
	assert.Equal(t, "rev-0", active.Services["web"].EffectiveRevision,
		"the rebuild reuses the pinned revision")

	op, err := store.LoadOperation(context.Background(), "blog", "op-restart-missing")
	require.NoError(t, err)
	assert.Equal(t, domain.AppOutcomeSuccess, op.Outcome)
	step, found := opStep(op, "service.web.restart")
	require.True(t, found)
	assert.Equal(t, domain.AppStepSucceeded, step.State)
	assert.Equal(t, "c-rebuilt", step.After)
}
