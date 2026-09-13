package deployment_test

import (
	"context"
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

// orderedTraffic records traffic boundary calls into a shared event log and
// can fail one withdrawal or one graph apply.
type orderedTraffic struct {
	mu           sync.Mutex
	events       *[]string
	failWithdraw bool
	failRebuild  bool
}

func (t *orderedTraffic) record(event string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.events != nil {
		*t.events = append(*t.events, event)
	}
}

func (t *orderedTraffic) RebuildTraffic(context.Context) error {
	t.record("traffic")
	if t.failRebuild {
		return assert.AnError
	}
	return nil
}

func (t *orderedTraffic) WithdrawService(context.Context, string, string) error {
	t.record("withdraw")
	if t.failWithdraw {
		return assert.AnError
	}
	return nil
}

func (t *orderedTraffic) WithdrawServiceState(context.Context, string, string) error {
	t.record("withdraw-state")
	if t.failWithdraw {
		return assert.AnError
	}
	return nil
}

// replaceableActive is one ACTIVE record whose web service is superseded.
func replaceableActive() domain.AppActive {
	return domain.AppActive{App: "blog", Services: map[string]domain.AppEffectiveService{
		"web": {EffectiveRevision: "rev-0", Image: "img:0", Container: "c-old"},
	}}
}

// recordEvent appends one step to a shared event log. Test doubles and mock
// hooks share the log, so the sequence a deployment performs is observable.
func recordEvent(events *[]string, event string) {
	if events != nil {
		*events = append(*events, event)
	}
}

// expectReplacementPreflight wires the preflight calls of one replacement
// without pre-registering an operation-journal write, so a test can observe
// the terminal journal write itself.
func expectReplacementPreflight(
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	images *outmocks.MockImageResolver,
	secrets *outmocks.MockSecretProvider,
	rev domain.AppDesiredRevision,
) {
	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, rev.Spec.Services[0].Image).Return(
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil)
	secrets.EXPECT().GetSecret(mock.Anything, "gordon/apps/app-blog/web/database-url").Return("x", nil)
	runtime.EXPECT().InspectImageVolumes(mock.Anything, rev.Spec.Services[0].Image).Return(nil, nil)
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
}

// expectSequentialCreate wires one replacement's candidate creation and loopback
// publication. It records "create-new" and "start-new" in order.
func expectSequentialCreate(
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	events *[]string,
	containerID string,
	hostPort int,
) {
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).RunAndReturn(
		func(context.Context, *domain.ContainerConfig) (*domain.Container, error) {
			recordEvent(events, "create-new")
			return &domain.Container{ID: containerID, Name: "web"}, nil
		}).Once()
	runtime.EXPECT().StartContainer(mock.Anything, containerID).RunAndReturn(
		func(context.Context, string) error {
			recordEvent(events, "start-new")
			return nil
		}).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, containerID, mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: hostPort, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
}

// expectRetireOld wires the confirmed retirement of the superseded container.
func expectRetireOld(
	state *outmocks.MockAppState,
	runtime *outmocks.MockContainerRuntime,
	events *[]string,
	containerID string,
) {
	runtime.EXPECT().StopContainer(mock.Anything, containerID, mock.Anything).RunAndReturn(
		func(context.Context, string, time.Duration) error {
			recordEvent(events, "stop-old")
			return nil
		}).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, containerID, false).RunAndReturn(
		func(context.Context, string, bool) error {
			recordEvent(events, "remove-old")
			return nil
		}).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", containerID).Return(nil).Once()
}

// TestDeploy_SequentialReplacementNeverOverlapsGenerations proves the
// replacement order for a public HTTP service without volumes: withdrawal is
// confirmed first, the superseded container is stopped and removed before the
// replacement is created, readiness runs before ACTIVE is published, and the
// graph is applied last.
func TestDeploy_SequentialReplacementNeverOverlapsGenerations(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	var order []string
	expectRetireOld(state, runtime, &order, "c-old")
	expectSequentialCreate(state, runtime, &order, "c-new", 18080)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(
		func(context.Context, domain.AppActive) error {
			recordEvent(&order, "publish-active")
			return nil
		}).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)
	state.EXPECT().ClearRecoveryInhibition(mock.Anything, "blog", "web", "c-old").Return(nil).Once()

	var probedURL string
	traffic := &orderedTraffic{events: &order}
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(_ context.Context, url string) (int, error) {
			probedURL = url
			recordEvent(&order, "ready")
			return 200, nil
		},
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Contains(t, probedURL, "127.0.0.1:18080", "readiness dials the replacement's fresh loopback bind")
	assert.Equal(t,
		[]string{"withdraw", "stop-old", "remove-old", "create-new", "start-new", "ready", "publish-active", "traffic"},
		order)
}

// TestDeploy_PreflightFailureLeavesTheWorkloadUntouched proves preflight runs
// to completion before anything is disrupted: an unresolvable image fails the
// deploy without withdrawing traffic or stopping, removing, creating, or
// starting any container.
func TestDeploy_PreflightFailureLeavesTheWorkloadUntouched(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	state.EXPECT().Recover(mock.Anything).Return(nil).Once()
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil).Once()
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	images.EXPECT().ResolveDigest(mock.Anything, mock.Anything).Return("", assert.AnError).Once()
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).Return(nil)

	var events []string
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &orderedTraffic{events: &events},
	}, zerowrap.Default())

	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.ErrorIs(t, err, domain.ErrAppImageUnresolvable)
	assert.Empty(t, events, "preflight must not touch traffic before it succeeds")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "ListNetworks", mock.Anything)
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
}

// TestDeploy_WithdrawalFailureBlocksMutation proves a traffic withdrawal that
// cannot be applied leaves the running generation untouched: no container is
// stopped, removed, created, or started, and no ACTIVE state is published.
func TestDeploy_WithdrawalFailureBlocksMutation(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	expectDeployPreflight(state, runtime, images, secrets, rev)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &orderedTraffic{failWithdraw: true},
	}, zerowrap.Default())

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Contains(t, result.Services["web"].Error, "withdraw")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "CreateVolume", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "ListNetworks", mock.Anything)
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, mock.Anything, mock.Anything)
}

// TestDeploy_FirstDeploymentRetiresNothing proves a service with no previous
// ACTIVE generation is created, verified, and published without any
// withdrawal or retirement step.
func TestDeploy_FirstDeploymentRetiresNothing(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()

	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	var order []string
	expectSequentialCreate(state, runtime, &order, "c-new", 18080)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().SaveActive(mock.Anything, mock.Anything).RunAndReturn(
		func(context.Context, domain.AppActive) error {
			recordEvent(&order, "publish-active")
			return nil
		}).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)

	traffic := &orderedTraffic{events: &order}
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) {
			recordEvent(&order, "ready")
			return 200, nil
		},
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["web"].Result)
	assert.Equal(t,
		[]string{"create-new", "start-new", "ready", "publish-active", "traffic"},
		order,
		"a first deployment withdraws nothing and retires nothing")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, mock.Anything, mock.Anything)
}

// TestDeploy_ReadinessFailureJournalsFailureAndRemovesCandidate proves a
// replacement that is not ready is journaled as failed, that ACTIVE is never
// published for it, and that the unready candidate is removed. The superseded
// container is already gone and is never recreated.
func TestDeploy_ReadinessFailureJournalsFailureAndRemovesCandidate(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()
	rev.Spec.Services[0].Readiness.Timeout = 50 * time.Millisecond

	expectReplacementPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	var order []string
	expectRetireOld(state, runtime, &order, "c-old")
	expectSequentialCreate(state, runtime, &order, "c-new", 18080)
	runtime.EXPECT().GetContainerLogs(mock.Anything, "c-new", false).Return(nil, assert.AnError).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", true).RunAndReturn(
		func(context.Context, string, bool) error {
			recordEvent(&order, "remove-candidate")
			return nil
		}).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-new").Return(nil).Once()

	var saved []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, op domain.AppOperation) error {
			saved = append(saved, op)
			return nil
		})

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
		Traffic: &orderedTraffic{events: &order},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 503, nil },
		func(context.Context, string) error { return assert.AnError },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "failed", result.Services["web"].Result)
	assert.Contains(t, result.Services["web"].Error, "readiness", "the failure names the readiness probe")
	assert.NotContains(t, result.Services["web"].Error, "logs:", "raw logs must never be embedded in the public error")
	assert.Equal(t, "c-new", result.Services["web"].After)

	// ACTIVE was never repointed and the replaced generation's inhibition was
	// never cleared: nothing claims the old workload is available again.
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
	state.AssertNotCalled(t, "ClearRecoveryInhibition", mock.Anything, mock.Anything, mock.Anything, mock.Anything)

	require.NotEmpty(t, saved)
	last := saved[len(saved)-1]
	assert.Equal(t, domain.AppOutcomeFailed, last.Outcome, "a failed replacement must not be journaled as success")
	step, ok := opStep(last, "service.web.replace")
	require.True(t, ok)
	assert.Equal(t, domain.AppStepFailed, step.State)

	assert.Equal(t, "remove-candidate", order[len(order)-1], "the unready candidate is the last thing removed")
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, "c-new", mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-old", true)
}

// TestDeploy_CandidateCleanupFailureStaysVisible proves a candidate that
// cannot be removed does not turn a failed replacement into a success: the
// leftover is reported as a cleanup warning on the failed service.
func TestDeploy_CandidateCleanupFailureStaysVisible(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()
	rev.Spec.Services[0].Readiness.Timeout = 50 * time.Millisecond

	expectDeployPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	expectRetireOld(state, runtime, nil, "c-old")
	expectSequentialCreate(state, runtime, nil, "c-new", 18080)
	runtime.EXPECT().GetContainerLogs(mock.Anything, "c-new", false).Return(nil, assert.AnError).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", true).Return(assert.AnError).Once()

	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 503, nil },
		func(context.Context, string) error { return assert.AnError },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	require.NotNil(t, result)
	svcResult := result.Services["web"]
	assert.Equal(t, "failed", svcResult.Result)
	require.Len(t, svcResult.CleanupWarnings, 1, "an unremovable candidate is reported, never hidden")
	assert.Equal(t, "web", svcResult.CleanupWarnings[0].Service)
	assert.Equal(t, "c-new", svcResult.CleanupWarnings[0].Leftover)
	assert.Contains(t, svcResult.CleanupWarnings[0].Detail, "remove")
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, "blog", "c-new")
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
}

// TestDeploy_CancellationDuringReadinessIsNotReportedAsSuccess proves a
// cancellation during the readiness wait never becomes a success: ACTIVE is
// not published, no terminal outcome is written, and the candidate cleanup
// that the canceled context could not complete is reported as a leftover.
// The durable store and the runtime refuse work on a canceled context, so
// this test models both.
func TestDeploy_CancellationDuringReadinessIsNotReportedAsSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state, runtime, images, secrets := mockDeps(t)
	rev := mockRevision()
	rev.Spec.Services[0].Readiness.Timeout = 30 * time.Second

	expectReplacementPreflight(state, runtime, images, secrets, rev)
	expectNetworkProvision(runtime, "app-blog", 1)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(replaceableActive(), true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)

	expectRetireOld(state, runtime, nil, "c-old")
	expectSequentialCreate(state, runtime, nil, "c-new", 18080)
	// The runtime refuses to remove the candidate with a canceled context.
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", true).Return(context.Canceled).Once()

	var saved []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(
		func(opCtx context.Context, op domain.AppOperation) error {
			if opCtx.Err() != nil {
				// The durable store refuses writes on a canceled context, so
				// the operation stays in flight instead of becoming terminal.
				return opCtx.Err()
			}
			saved = append(saved, op)
			return nil
		})

	probeStarted := make(chan struct{})
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets,
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
	var deployResult *deployment.DeployResult
	go func() {
		result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
		deployResult = result
		done <- err
	}()
	select {
	case <-probeStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the readiness probe")
	}
	cancel()
	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), context.Canceled.Error(), "the canceled probe is reported")

	// No success is claimed anywhere: ACTIVE is untouched, and no journaled
	// outcome records the replacement as succeeded. The durable store refuses
	// the terminal write on a canceled context, so the operation stays in
	// flight rather than becoming a false success.
	state.AssertNotCalled(t, "SaveActive", mock.Anything, mock.Anything)
	require.NotEmpty(t, saved)
	for _, op := range saved {
		assert.NotContains(t, []string{domain.AppOutcomeSuccess, domain.AppOutcomePartial}, op.Outcome,
			"a canceled replacement is never journaled as a success")
		if step, ok := opStep(op, "service.web.replace"); ok {
			assert.NotEqual(t, domain.AppStepSucceeded, step.State,
				"a canceled replacement step is never recorded as succeeded")
		}
	}

	// The canceled cleanup is best effort, and what it could not finish is
	// reported instead of being hidden.
	require.NotNil(t, deployResult)
	require.Len(t, deployResult.Services["web"].CleanupWarnings, 1,
		"a candidate the canceled cleanup could not remove is reported")
	assert.Equal(t, "c-new", deployResult.Services["web"].CleanupWarnings[0].Leftover)
	runtime.AssertCalled(t, "RemoveContainer", mock.Anything, "c-new", true)
	state.AssertNotCalled(t, "ReleaseBackendBinds", mock.Anything, "blog", "c-new")
}

// TestDeploy_MultiServiceStopsAtFirstFailure proves services are deployed in
// sorted order and the first failure stops the run: the failing service is
// cleaned up, later services are untouched, and the services already deployed
// are kept without any rollback.
func TestDeploy_MultiServiceStopsAtFirstFailure(t *testing.T) {
	ctx := context.Background()
	state, runtime, images, secrets := mockDeps(t)

	httpService := func(name, host, image string, timeout time.Duration) domain.AppService {
		return domain.AppService{
			Name: name, Image: image,
			Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: timeout},
			HTTP:      []domain.AppHTTPInterface{{Host: host, Port: 8080, TLS: domain.AppTLSAuto}},
		}
	}
	rev := testRevision("blog",
		httpService("alpha", "alpha.example.com", "docker.io/example/alpha:1.4.2", time.Second),
		httpService("beta", "beta.example.com", "docker.io/example/beta:1.4.2", 50*time.Millisecond),
		httpService("gamma", "gamma.example.com", "docker.io/example/gamma:1.4.2", time.Second),
	)

	state.EXPECT().Recover(mock.Anything).Return(nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/alpha:1.4.2").Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/beta:1.4.2").Return("sha256:"+strings.Repeat("b", 64), nil).Once()
	images.EXPECT().ResolveDigest(mock.Anything, "docker.io/example/gamma:1.4.2").Return("sha256:"+strings.Repeat("c", 64), nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/alpha:1.4.2").Return(nil, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/beta:1.4.2").Return(nil, nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "docker.io/example/gamma:1.4.2").Return(nil, nil).Once()
	state.EXPECT().LoadCheckpoint(mock.Anything).Return(domain.AppStoreCheckpoint{}, nil)
	state.EXPECT().LoadOwnership(mock.Anything, "blog").Return(domain.AppOwnership{App: "blog", ID: "app-blog"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "blog").Return(domain.AppActive{
		App: "blog", Services: map[string]domain.AppEffectiveService{},
	}, true, nil)
	state.EXPECT().LoadDesired(mock.Anything, "blog").Return(rev, true, nil)
	expectNetworkProvision(runtime, "app-blog", 2)

	expectSequentialCreate(state, runtime, nil, "c-alpha", 18080)
	state.EXPECT().SaveActive(mock.Anything, mock.MatchedBy(func(active domain.AppActive) bool {
		return active.Services["alpha"].Container == "c-alpha"
	})).Return(nil).Once()
	state.EXPECT().SaveOwnership(mock.Anything, mock.Anything).Return(nil)

	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-beta", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	state.EXPECT().RegisterBackendBinds(mock.Anything, mock.Anything).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "c-beta", Name: "beta"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-beta").Return(nil).Once()
	runtime.EXPECT().GetContainerLogs(mock.Anything, "c-beta", false).Return(nil, assert.AnError).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-beta", true).Return(nil).Once()
	state.EXPECT().ReleaseBackendBinds(mock.Anything, "blog", "c-beta").Return(nil).Once()

	var saved []domain.AppOperation
	state.EXPECT().SaveOperation(mock.Anything, mock.Anything).RunAndReturn(
		func(_ context.Context, op domain.AppOperation) error {
			saved = append(saved, op)
			return nil
		})

	traffic := &orderedTraffic{}
	svc := deployment.NewService(deployment.Deps{
		State: state, Runtime: runtime, Images: images, Secrets: secrets, Traffic: traffic,
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(_ context.Context, url string) (int, error) {
			if strings.Contains(url, "18081") {
				return 503, nil
			}
			return 200, nil
		},
		func(context.Context, string) error { return nil },
	))

	result, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "deployed", result.Services["alpha"].Result, "an already deployed service is kept")
	assert.Equal(t, "failed", result.Services["beta"].Result)
	assert.NotContains(t, result.Services, "gamma", "a later service is never attempted after a failure")

	runtime.AssertNumberOfCalls(t, "CreateContainer", 2)
	runtime.AssertNotCalled(t, "CreateContainer", mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.Name == "gamma"
	}))
	// No rollback of the already deployed service.
	runtime.AssertNotCalled(t, "StopContainer", mock.Anything, mock.Anything, mock.Anything)
	runtime.AssertNotCalled(t, "RemoveContainer", mock.Anything, "c-alpha", mock.Anything)
	state.AssertNumberOfCalls(t, "SaveActive", 1)

	require.NotEmpty(t, saved)
	last := saved[len(saved)-1]
	assert.Equal(t, domain.AppOutcomePartial, last.Outcome,
		"the run stops at the first failure; the earlier success is kept, not rolled back")
}

// TestDeploy_VolumeReplacementPreventsOverlappingWriters proves a stateful
// replacement durably inhibits the superseded generation before it is
// retired, stops and removes it before the candidate starts, and keeps the
// inhibition when the candidate never becomes ready. Exclusion holds because
// the previous process is gone before the replacement can write.
func TestDeploy_VolumeReplacementPreventsOverlappingWriters(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	runtime := outmocks.NewMockContainerRuntime(t)
	images := outmocks.NewMockImageResolver(t)
	secrets := outmocks.NewMockSecretProvider(t)

	image := "registry.example.com/blog/web:1.4.2"
	spec := domain.AppService{
		Name:    "web",
		Image:   image,
		Secrets: map[string]string{},
		Volumes: []domain.AppVolume{{Name: "data", Path: "/data"}},
		HTTP:    []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: domain.AppTLSAuto}},
		Readiness: domain.AppReadiness{
			Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: 50 * time.Millisecond,
		},
	}
	rev := testRevision("blog", spec)

	require.NoError(t, store.SaveIntent(ctx, domain.AppStopIntent{App: "blog"}))
	require.NoError(t, store.SaveActive(ctx, domain.AppActive{
		App: "blog",
		Services: map[string]domain.AppEffectiveService{
			"web": {
				Container: "c-old", EffectiveRevision: "rev-0", Spec: spec,
				BackendBinds: map[int]int{8080: 18080},
			},
		},
	}))
	seedDesired(t, ctx, store, rev)
	// The app keeps one incarnation: the private network every service joins
	// is derived from it.
	require.NoError(t, store.SaveOwnership(ctx, domain.AppOwnership{App: "blog", ID: "app-blog"}))
	expectNetworkProvision(runtime, "app-blog", 1)

	images.EXPECT().ResolveDigest(mock.Anything, image).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil).Once()
	runtime.EXPECT().InspectImageVolumes(mock.Anything, image).Return(nil, nil).Once()
	runtime.EXPECT().VolumeExists(mock.Anything, mock.Anything).Return(false, nil).Once()

	var order []string
	inhibitedBeforeStop := false
	runtime.EXPECT().StopContainer(mock.Anything, "c-old", mock.Anything).RunAndReturn(
		func(context.Context, string, time.Duration) error {
			recordEvent(&order, "stop-old")
			// The durable marker must already exist before the superseded
			// writer is stopped: recovery must never race the destructive step.
			inhibitions, loadErr := store.LoadRecoveryInhibitions(context.Background(), "blog")
			require.NoError(t, loadErr)
			inhibitedBeforeStop = len(inhibitions) == 1 &&
				inhibitions[0].ContainerID == "c-old" &&
				inhibitions[0].Reason == domain.AppInhibitReplacementPending
			return nil
		}).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-old", false).RunAndReturn(
		func(context.Context, string, bool) error {
			recordEvent(&order, "remove-old")
			return nil
		}).Once()
	runtime.EXPECT().CreateVolume(mock.Anything, mock.Anything, mock.Anything).RunAndReturn(
		func(context.Context, string, map[string]string) error {
			recordEvent(&order, "create-volume")
			return nil
		}).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).RunAndReturn(
		func(context.Context, *domain.ContainerConfig) (*domain.Container, error) {
			recordEvent(&order, "create-new")
			return &domain.Container{ID: "c-new", Name: "web"}, nil
		}).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "c-new").RunAndReturn(
		func(context.Context, string) error {
			recordEvent(&order, "start-new")
			return nil
		}).Once()
	runtime.EXPECT().GetContainerBackendBinds(mock.Anything, "c-new", mock.Anything).Return(
		[]domain.ContainerBackendBind{{ContainerPort: 8080, HostPort: 18081, Protocol: domain.NetworkProtocolTCP}}, nil).Once()
	runtime.EXPECT().GetContainerLogs(mock.Anything, "c-new", false).Return(nil, assert.AnError).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "c-new", true).Return(nil).Once()

	svc := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
		ImagePolicy: domain.ImageSourcePolicy{AllowedRegistries: []string{"registry.example.com"}},
	}, zerowrap.Default()).WithProbeDeps(deployment.NewTestProbeDeps(runtime,
		func(context.Context, string) (int, error) { return 503, nil },
		func(context.Context, string) error { return assert.AnError },
	))

	_, err := svc.Deploy(ctx, deployment.DeployInput{App: "blog"})
	require.Error(t, err)

	assert.Equal(t, []string{"stop-old", "remove-old", "create-volume", "create-new", "start-new"}, order,
		"the superseded writer is gone before the replacement can write")
	assert.True(t, inhibitedBeforeStop,
		"the durable recovery inhibition exists before the superseded writer is stopped")

	// The failed replacement never cleared the durable inhibition: a later
	// boot must not revive the old writer on top of data the candidate may
	// already have touched.
	inhibitions, loadErr := store.LoadRecoveryInhibitions(ctx, "blog")
	require.NoError(t, loadErr)
	require.Len(t, inhibitions, 1)
	assert.Equal(t, "c-old", inhibitions[0].ContainerID)
	assert.Equal(t, domain.AppInhibitReplacementPending, inhibitions[0].Reason)

	// ACTIVE still names the superseded generation: the failure is explicit
	// and no availability was restored on its behalf.
	active, ok, loadErr := store.LoadActive(ctx, "blog")
	require.NoError(t, loadErr)
	require.True(t, ok)
	assert.Equal(t, "c-old", active.Services["web"].Container)

	rebooted := deployment.NewService(deployment.Deps{
		State: store, Runtime: runtime, Images: images, Secrets: secrets,
	}, zerowrap.Default())
	bootErr := rebooted.ReconcileBoot(ctx)
	require.Error(t, bootErr)
	assert.Contains(t, bootErr.Error(), "recovery inhibited")
	runtime.AssertNotCalled(t, "StartContainer", mock.Anything, "c-old")
	runtime.AssertNotCalled(t, "RestartContainer", mock.Anything, "c-old", mock.Anything)
}
