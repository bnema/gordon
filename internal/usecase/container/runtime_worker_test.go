package container

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeWorkerCommandTranslation(t *testing.T) {
	ctx := context.Background()
	identity := testRuntimeCommandIdentity("cmd-1")
	fake := &fakeRuntimeWorkerService{}
	worker := NewRuntimeWorker(fake)

	deploy, err := worker.DeployRoute(ctx, domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest", Env: []string{"A=1"}})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, deploy.Status)
	assert.Equal(t, "Deploy:app.example.com:app:latest", fake.calls[0])
	assert.Equal(t, []string{"A=1"}, fake.deployRoute.Env)

	restart, err := worker.RestartRoute(ctx, domain.RestartRouteCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-2"), Domain: "app.example.com", WithAttachments: true})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, restart.Status)
	assert.Equal(t, "Restart:app.example.com:true", fake.calls[1])

	remove, err := worker.RemoveRoute(ctx, domain.RemoveRouteCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-3"), Domain: "app.example.com"})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, remove.Status)
	assert.Equal(t, "Remove:app.example.com", fake.calls[2])

	desired := []domain.Route{{Domain: "app.example.com", Image: "app:latest"}}
	reconcile, err := worker.Reconcile(ctx, domain.ReconcileRuntimeCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-4"), ExpectedRouteCount: len(desired), DesiredRoutes: desired})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, reconcile.Status)
	assert.Equal(t, []string{"Sync", "AutoStart:1"}, fake.calls[3:])
	assert.Equal(t, desired, fake.autoStartRoutes)
}

func TestRuntimeWorkerResultSanitizedErrorAndCancellation(t *testing.T) {
	identity := testRuntimeCommandIdentity("cmd-err")
	fake := &fakeRuntimeWorkerService{deployErr: errors.New("failed with SECRET=value and /var/run/docker.sock")}
	worker := NewRuntimeWorker(fake)

	result, err := worker.DeployRoute(context.Background(), domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"})
	require.NoError(t, err)
	assert.Equal(t, identity.ID, result.CommandID)
	assert.Equal(t, identity.IdempotencyKey, result.IdempotencyKey)
	assert.Equal(t, identity.Generation, result.Generation)
	assert.Equal(t, domain.RuntimeCommandStatusFailed, result.Status)
	require.NotNil(t, result.Error)
	assert.Equal(t, "runtime_command_failed", result.Error.Code)
	assert.Equal(t, "runtime command failed", result.Error.Message)
	assert.NotContains(t, result.Error.Message, "SECRET")
	assert.NotContains(t, result.Error.Message, "/var/run")

	tests := []struct {
		name    string
		invoke  func(*RuntimeWorker) (domain.RuntimeCommandResult, error)
		wantOps []string
	}{
		{name: "deploy", invoke: func(w *RuntimeWorker) (domain.RuntimeCommandResult, error) {
			return w.DeployRoute(context.Background(), domain.DeployRouteCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-cancel-deploy"), Domain: "app.example.com", Image: "app:latest"})
		}, wantOps: []string{"Deploy:app.example.com:app:latest"}},
		{name: "restart", invoke: func(w *RuntimeWorker) (domain.RuntimeCommandResult, error) {
			return w.RestartRoute(context.Background(), domain.RestartRouteCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-cancel-restart"), Domain: "app.example.com"})
		}, wantOps: []string{"Restart:app.example.com:false"}},
		{name: "remove", invoke: func(w *RuntimeWorker) (domain.RuntimeCommandResult, error) {
			return w.RemoveRoute(context.Background(), domain.RemoveRouteCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-cancel-remove"), Domain: "app.example.com"})
		}, wantOps: []string{"Remove:app.example.com"}},
		{name: "reconcile", invoke: func(w *RuntimeWorker) (domain.RuntimeCommandResult, error) {
			return w.Reconcile(context.Background(), domain.ReconcileRuntimeCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-cancel-reconcile"), ExpectedRouteCount: 1, DesiredRoutes: []domain.Route{{Domain: "app.example.com", Image: "app:latest"}}})
		}, wantOps: []string{"Sync"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRuntimeWorkerService{deployErr: context.Canceled, restartErr: context.Canceled, removeErr: context.Canceled, syncErr: context.Canceled}
			worker := NewRuntimeWorker(fake)
			canceled, err := tt.invoke(worker)
			require.NoError(t, err)
			assert.Equal(t, domain.RuntimeCommandStatusFailed, canceled.Status)
			require.NotNil(t, canceled.Error)
			assert.Equal(t, "context_canceled", canceled.Error.Code)
			assert.False(t, canceled.Error.Retryable)
			assert.Equal(t, tt.wantOps, fake.calls)
		})
	}
}

func TestSanitizeRuntimeErrorMessageCaseInsensitive(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want string
	}{
		{name: "uppercase unix socket", msg: "dial UNIX:///VAR/RUN/DOCKER.SOCK", want: "runtime command failed"},
		{name: "mixed case unix socket", msg: "dial UnIx://docker-endpoint", want: "runtime command failed"},
		{name: "uppercase var run path", msg: "open /VAR/RUN/docker.sock", want: "runtime command failed"},
		{name: "mixed case run path", msg: "open /RuN/docker.sock", want: "runtime command failed"},
		{name: "mixed case token", msg: "request failed with ToKeN=value", want: "runtime command failed"},
		{name: "safe message", msg: "container image was not found", want: "container image was not found"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sanitizeRuntimeErrorMessage(errors.New(tt.msg)))
		})
	}
}

func TestRuntimeWorkerDeadlineExceededIsFailedAndRetryable(t *testing.T) {
	fake := &fakeRuntimeWorkerService{deployErr: context.DeadlineExceeded}
	worker := NewRuntimeWorker(fake)

	result, err := worker.DeployRoute(context.Background(), domain.DeployRouteCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-deadline"),
		Domain:                 "app.example.com",
		Image:                  "app:latest",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusFailed, result.Status)
	require.NotNil(t, result.Error)
	assert.Equal(t, "context_deadline_exceeded", result.Error.Code)
	assert.True(t, result.Error.Retryable)
}

func TestRuntimeWorkerIdempotencyReturnsCachedResult(t *testing.T) {
	identity := testRuntimeCommandIdentity("cmd-dedupe")
	fake := &fakeRuntimeWorkerService{}
	worker := NewRuntimeWorker(fake)
	command := domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"}

	first, err := worker.DeployRoute(context.Background(), command)
	require.NoError(t, err)
	second, err := worker.DeployRoute(context.Background(), command)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, []string{"Deploy:app.example.com:app:latest"}, fake.calls)
}

func TestRuntimeWorkerRetriesFailedCommandsButCachesSucceededAndPolicyDeniedResults(t *testing.T) {
	identity := testRuntimeCommandIdentity("cmd-retry")
	fake := &fakeRuntimeWorkerService{deployErr: errors.New("temporary failure")}
	worker := NewRuntimeWorker(fake)
	command := domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"}

	first, err := worker.DeployRoute(context.Background(), command)
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusFailed, first.Status)
	fake.deployErr = nil
	second, err := worker.DeployRoute(context.Background(), command)
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, second.Status)
	assert.Equal(t, []string{"Deploy:app.example.com:app:latest", "Deploy:app.example.com:app:latest"}, fake.calls)

	redelivery := command
	redelivery.Generation++
	third, err := worker.DeployRoute(context.Background(), redelivery)
	require.NoError(t, err)
	assert.Equal(t, second, third)
	assert.Len(t, fake.calls, 2)

	deniedWorker := NewRuntimeWorkerWithPolicy(&fakeRuntimeWorkerService{}, RuntimePolicy{Mode: RuntimePolicyModeEnforce, RequireImageDigest: true})
	deniedCommand := domain.DeployRouteCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-denied"), Domain: "app.example.com", Image: "app:latest"}
	denied, err := deniedWorker.DeployRoute(context.Background(), deniedCommand)
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusDenied, denied.Status)
	require.NotNil(t, denied.Error)
	assert.Equal(t, "runtime_policy_denied:image_digest_required", denied.Error.Code)
	assert.False(t, denied.Error.Retryable)
	cachedDenied, err := deniedWorker.DeployRoute(context.Background(), deniedCommand)
	require.NoError(t, err)
	assert.Equal(t, denied, cachedDenied)
	assert.Len(t, deniedWorker.PolicyDeniedEvents(), 1)
}

func TestRuntimeWorkerCompletedResultsAreBoundedAndKeepRecentKeys(t *testing.T) {
	fake := &fakeRuntimeWorkerService{}
	worker := NewRuntimeWorker(fake)

	for i := range runtimeWorkerCompletedResultLimit + 5 {
		identity := testRuntimeCommandIdentity("cmd-bounded-" + strconv.Itoa(i))
		_, err := worker.DeployRoute(context.Background(), domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"})
		require.NoError(t, err)
	}

	worker.mu.Lock()
	cachedCount := len(worker.completedByDedupeKey)
	worker.mu.Unlock()
	assert.LessOrEqual(t, cachedCount, runtimeWorkerCompletedResultLimit)

	callsBefore := len(fake.calls)
	recent := testRuntimeCommandIdentity("cmd-bounded-" + strconv.Itoa(runtimeWorkerCompletedResultLimit+4))
	_, err := worker.DeployRoute(context.Background(), domain.DeployRouteCommand{RuntimeCommandIdentity: recent, Domain: "app.example.com", Image: "app:latest"})
	require.NoError(t, err)
	assert.Len(t, fake.calls, callsBefore)
}

func TestRuntimeWorkerPolicyDeniedEventsAreBounded(t *testing.T) {
	worker := NewRuntimeWorkerWithPolicy(&fakeRuntimeWorkerService{}, RuntimePolicy{Mode: RuntimePolicyModeObserve, RequireImageDigest: true})

	for i := range runtimeWorkerPolicyDeniedEventLimit + 5 {
		identity := testRuntimeCommandIdentity("cmd-policy-bounded-" + strconv.Itoa(i))
		_, err := worker.DeployRoute(context.Background(), domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"})
		require.NoError(t, err)
	}

	events := worker.PolicyDeniedEvents()
	require.Len(t, events, runtimeWorkerPolicyDeniedEventLimit)
	assert.Equal(t, domain.RuntimeCommandID("cmd-policy-bounded-5"), events[0].CommandID)
	assert.Equal(t, domain.RuntimeCommandID("cmd-policy-bounded-"+strconv.Itoa(runtimeWorkerPolicyDeniedEventLimit+4)), events[len(events)-1].CommandID)
}

func TestRuntimeWorkerConcurrentDuplicateCommandsRunMutationOnce(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(context.Context, *RuntimeWorker, domain.RuntimeCommandIdentity) (domain.RuntimeCommandResult, error)
		calls  func(*blockingRuntimeWorkerService) int64
	}{
		{name: "Deploy", invoke: func(ctx context.Context, w *RuntimeWorker, identity domain.RuntimeCommandIdentity) (domain.RuntimeCommandResult, error) {
			return w.DeployRoute(ctx, domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"})
		}, calls: func(f *blockingRuntimeWorkerService) int64 { return f.deployCalls.Load() }},
		{name: "Restart", invoke: func(ctx context.Context, w *RuntimeWorker, identity domain.RuntimeCommandIdentity) (domain.RuntimeCommandResult, error) {
			return w.RestartRoute(ctx, domain.RestartRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com"})
		}, calls: func(f *blockingRuntimeWorkerService) int64 { return f.restartCalls.Load() }},
		{name: "Remove", invoke: func(ctx context.Context, w *RuntimeWorker, identity domain.RuntimeCommandIdentity) (domain.RuntimeCommandResult, error) {
			return w.RemoveRoute(ctx, domain.RemoveRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com"})
		}, calls: func(f *blockingRuntimeWorkerService) int64 { return f.removeCalls.Load() }},
		{name: "Reconcile", invoke: func(ctx context.Context, w *RuntimeWorker, identity domain.RuntimeCommandIdentity) (domain.RuntimeCommandResult, error) {
			return w.Reconcile(ctx, domain.ReconcileRuntimeCommand{RuntimeCommandIdentity: identity, ExpectedRouteCount: 1, DesiredRoutes: []domain.Route{{Domain: "app.example.com", Image: "app:latest"}}})
		}, calls: func(f *blockingRuntimeWorkerService) int64 { return f.syncCalls.Load() }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newBlockingRuntimeWorkerService()
			worker := NewRuntimeWorker(fake)
			identity := testRuntimeCommandIdentity("cmd-concurrent-" + tt.name)
			start := make(chan struct{})
			results := make(chan domain.RuntimeCommandResult, 8)
			errs := make(chan error, 8)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					result, err := tt.invoke(context.Background(), worker, identity)
					results <- result
					errs <- err
				}()
			}

			close(start)
			select {
			case <-fake.entered:
			case <-time.After(time.Second):
				t.Fatal("mutation was not invoked")
			}
			require.Eventually(t, func() bool { return tt.calls(fake) == 1 }, 100*time.Millisecond, 10*time.Millisecond)
			assert.Equal(t, int64(1), tt.calls(fake))

			close(fake.release)
			wg.Wait()
			close(results)
			close(errs)
			var first *domain.RuntimeCommandResult
			for err := range errs {
				require.NoError(t, err)
			}
			for result := range results {
				assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
				if first == nil {
					first = &result
					continue
				}
				assert.Equal(t, *first, result)
			}

			cached, err := tt.invoke(context.Background(), worker, identity)
			require.NoError(t, err)
			require.NotNil(t, first)
			assert.Equal(t, *first, cached)
			assert.Equal(t, int64(1), tt.calls(fake))
		})
	}
}

func TestRuntimeWorkerConcurrentPolicyDeniedCommandsAreSerialized(t *testing.T) {
	const callers = 8

	previousMaxProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousMaxProcs)

	fake := newBlockingPolicyDeniedPublisherService()
	worker := NewRuntimeWorkerWithPolicy(fake, RuntimePolicy{Mode: RuntimePolicyModeEnforce, RequireImageDigest: true})
	command := domain.DeployRouteCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-concurrent-policy-denied"),
		Domain:                 "app.example.com",
		Image:                  "app:latest",
	}
	start := make(chan struct{})
	results := make(chan domain.RuntimeCommandResult, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := worker.DeployRoute(context.Background(), command)
			results <- result
			errs <- err
		}()
	}
	released := false
	defer func() {
		if !released {
			close(fake.release)
		}
		wg.Wait()
	}()

	close(start)
	select {
	case <-fake.publishEntered:
	case <-time.After(time.Second):
		t.Fatal("policy denial was not published")
	}
	assert.Never(t, func() bool { return fake.publications.Load() > 1 }, 100*time.Millisecond, time.Millisecond)

	close(fake.release)
	released = true
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		require.NoError(t, err)
	}
	var first *domain.RuntimeCommandResult
	for result := range results {
		assert.Equal(t, domain.RuntimeCommandStatusDenied, result.Status)
		if first == nil {
			first = &result
			continue
		}
		assert.Equal(t, *first, result)
	}
	require.NotNil(t, first)
	assert.Len(t, worker.PolicyDeniedEvents(), 1)
	assert.Equal(t, int64(1), fake.publications.Load())
}

func TestRuntimeWorkerConcurrentDuplicateWaitRespectsContextCancellation(t *testing.T) {
	fake := newBlockingRuntimeWorkerService()
	worker := NewRuntimeWorker(fake)
	identity := testRuntimeCommandIdentity("cmd-concurrent-cancel")
	leaderDone := make(chan struct{})
	go func() {
		defer close(leaderDone)
		_, _ = worker.DeployRoute(context.Background(), domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"})
	}()
	select {
	case <-fake.entered:
	case <-time.After(time.Second):
		t.Fatal("leader mutation was not invoked")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := worker.DeployRoute(ctx, domain.DeployRouteCommand{RuntimeCommandIdentity: identity, Domain: "app.example.com", Image: "app:latest"})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusFailed, result.Status)
	require.NotNil(t, result.Error)
	assert.Equal(t, "context_canceled", result.Error.Code)
	assert.False(t, result.Error.Retryable)
	assert.Equal(t, int64(1), fake.deployCalls.Load())

	close(fake.release)
	select {
	case <-leaderDone:
	case <-time.After(time.Second):
		t.Fatal("leader did not complete")
	}
}

func TestRuntimeWorkerSelfUpdateDeniedWithoutMutation(t *testing.T) {
	fake := &fakeRuntimeWorkerService{}
	worker := NewRuntimeWorker(fake)

	result, err := worker.SelfUpdate(context.Background(), domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-self"),
		TargetComponentID:      "runtime-1",
		TargetComponentRole:    domain.ComponentRoleRuntime,
		TargetVersion:          "v1.2.3",
		Policy:                 domain.RuntimeSelfUpdatePolicyManualApproval,
		PolicyDecisionID:       "decision-1",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusDenied, result.Status)
	require.NotNil(t, result.Error)
	assert.Equal(t, "self_update_unavailable", result.Error.Code)
	assert.Empty(t, fake.calls)
}

func TestRuntimeWorkerSnapshotSanitization(t *testing.T) {
	fake := &fakeRuntimeWorkerService{
		containers: map[string]*domain.Container{
			"c1": {ID: "c1", Name: "gordon-app.example.com", Image: "app:latest", ImageID: "sha256:abc", Status: "running", Ports: []int{8080}, Created: time.Unix(10, 0), Labels: map[string]string{domain.LabelDomain: "app.example.com", domain.LabelManaged: "true", domain.LabelProxyPort: "8080", domain.LabelProxyProtocol: "h2c", "com.example.secret": "do-not-leak"}},
			"c2": {ID: "c2", Name: "gordon-down.example.com", Image: "down:latest", Status: "exited", Labels: map[string]string{domain.LabelDomain: "down.example.com", "TOKEN": "secret"}},
		},
		routes: []domain.RouteInfo{
			{Domain: "app.example.com", ContainerID: "c1", ContainerStatus: "running", Network: "gordon-net"},
			{Domain: "down.example.com", ContainerID: "c2", ContainerStatus: "exited", Network: "gordon-net"},
		},
		networks: []*domain.NetworkInfo{{Name: "gordon-net", Driver: "bridge", Containers: []string{"gordon-target-app-example-com", "localhost", "127.0.0.1", "bad\\alias"}}},
		volumes: []*domain.VolumeInfo{
			{Name: "app-data", Containers: []string{"gordon-app.example.com", "/var/lib/secret", "bad\\alias"}},
			{Name: "/host/path", MountPoint: "/host/path", Containers: []string{"gordon-app.example.com"}},
			{Name: "docker.sock", MountPoint: "/run/user/1000/podman/podman.sock"},
		},
	}
	worker := NewRuntimeWorker(fake)
	worker.now = func() time.Time { return time.Unix(100, 0) }

	snapshot, err := worker.Snapshot(context.Background(), 7, "state-v1", "runtime-1")
	require.NoError(t, err)
	require.NoError(t, snapshot.Validate())
	assert.Equal(t, time.Unix(100, 0), snapshot.ObservedAt)
	require.Len(t, snapshot.Routes, 2)
	assert.Equal(t, domain.RouteTargetStatusReady, snapshot.Routes[0].Status)
	assert.Equal(t, "gordon-target-app-example-com", snapshot.Routes[0].EdgeTargetAlias)
	assert.Equal(t, "http", snapshot.Routes[0].Scheme)
	assert.Equal(t, domain.RouteTargetProtocolH2C, snapshot.Routes[0].Protocol)
	assert.NotContains(t, snapshot.Routes[0].EdgeTargetAlias, "localhost")
	assert.Equal(t, domain.RouteTargetStatusUnavailable, snapshot.Routes[1].Status)
	assert.Empty(t, snapshot.Routes[1].EdgeTargetAlias)
	require.Len(t, snapshot.Containers, 2)
	assert.NotContains(t, snapshot.Containers[0].Labels, "com.example.secret")
	assert.NotContains(t, snapshot.Containers[1].Labels, "TOKEN")
	require.Len(t, snapshot.Networks, 1)
	assert.Equal(t, []string{"gordon-target-app-example-com"}, snapshot.Networks[0].Aliases)
	require.Len(t, snapshot.Volumes, 1)
	assert.Equal(t, "app-data", snapshot.Volumes[0].Name)
	assert.Equal(t, []string{"gordon-app.example.com"}, snapshot.Volumes[0].AttachedTo)
	require.Len(t, snapshot.EdgeAttachments, 1)
	assert.Equal(t, "gordon-target-app-example-com", snapshot.EdgeAttachments[0].TargetAlias)
}

func TestRuntimeWorkerSnapshotUsesInstalledTargetAliasOrBackingContainerMembership(t *testing.T) {
	fake := &fakeRuntimeWorkerService{
		containers: map[string]*domain.Container{
			"c1": {ID: "c1", Name: "gordon-app.example.com", Image: "app:latest", Status: "running", Ports: []int{8080}, Labels: map[string]string{domain.LabelDomain: "app.example.com"}},
		},
		routes:   []domain.RouteInfo{{Domain: "app.example.com", ContainerID: "c1", ContainerStatus: "running", Network: "gordon-net"}},
		networks: []*domain.NetworkInfo{{Name: "gordon-net", Driver: "bridge", Containers: []string{"gordon-app.example.com"}}},
	}
	worker := NewRuntimeWorker(fake)

	snapshot, err := worker.Snapshot(context.Background(), 7, "state-v1", "runtime-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Routes, 1)
	assert.Equal(t, domain.RouteTargetStatusReady, snapshot.Routes[0].Status)
	assert.Equal(t, "gordon-target-app-example-com", snapshot.Routes[0].EdgeTargetAlias)
	require.Len(t, snapshot.EdgeAttachments, 1)
	assert.Equal(t, "gordon-target-app-example-com", snapshot.EdgeAttachments[0].TargetAlias)

	fake.networks = []*domain.NetworkInfo{{Name: "gordon-net", Driver: "bridge", Containers: []string{"unrelated"}}}
	snapshot, err = worker.Snapshot(context.Background(), 7, "state-v1", "runtime-1")
	require.NoError(t, err)
	require.Len(t, snapshot.Routes, 1)
	assert.Equal(t, domain.RouteTargetStatusUnavailable, snapshot.Routes[0].Status)
	assert.Empty(t, snapshot.Routes[0].EdgeTargetAlias)
	assert.Empty(t, snapshot.EdgeAttachments)
}

func testRuntimeCommandIdentity(id string) domain.RuntimeCommandIdentity {
	return domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID(id), IdempotencyKey: id + "-key", Generation: 1, SourceComponentID: "control-plane", RequestedAt: time.Unix(1, 0)}
}

type blockingRuntimeWorkerService struct {
	entered      chan struct{}
	release      chan struct{}
	deployCalls  atomic.Int64
	restartCalls atomic.Int64
	removeCalls  atomic.Int64
	syncCalls    atomic.Int64
}

func newBlockingRuntimeWorkerService() *blockingRuntimeWorkerService {
	return &blockingRuntimeWorkerService{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (f *blockingRuntimeWorkerService) Deploy(context.Context, domain.Route) (*domain.Container, error) {
	f.deployCalls.Add(1)
	f.enter()
	return &domain.Container{ID: "new"}, nil
}

func (f *blockingRuntimeWorkerService) Restart(context.Context, string, bool) error {
	f.restartCalls.Add(1)
	f.enter()
	return nil
}

func (f *blockingRuntimeWorkerService) ReconcileRemovedRoute(context.Context, string) (*domain.CleanupReport, error) {
	f.removeCalls.Add(1)
	f.enter()
	return &domain.CleanupReport{}, nil
}

func (f *blockingRuntimeWorkerService) SyncContainers(context.Context) error {
	f.syncCalls.Add(1)
	f.enter()
	return nil
}

func (f *blockingRuntimeWorkerService) AutoStart(context.Context, []domain.Route) error {
	return nil
}

func (f *blockingRuntimeWorkerService) enter() {
	select {
	case f.entered <- struct{}{}:
	default:
	}
	<-f.release
}

func (f *blockingRuntimeWorkerService) List(context.Context) map[string]*domain.Container {
	return nil
}
func (f *blockingRuntimeWorkerService) ListRoutesWithDetails(context.Context) []domain.RouteInfo {
	return nil
}
func (f *blockingRuntimeWorkerService) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	return nil, nil
}

type blockingPolicyDeniedPublisherService struct {
	*fakeRuntimeWorkerService
	publishEntered chan struct{}
	release        chan struct{}
	publications   atomic.Int64
}

func newBlockingPolicyDeniedPublisherService() *blockingPolicyDeniedPublisherService {
	return &blockingPolicyDeniedPublisherService{
		fakeRuntimeWorkerService: &fakeRuntimeWorkerService{},
		publishEntered:           make(chan struct{}, 8),
		release:                  make(chan struct{}),
	}
}

func (f *blockingPolicyDeniedPublisherService) PublishRuntimePolicyDeniedEvent(context.Context, domain.RuntimePolicyDeniedEvent) error {
	f.publications.Add(1)
	f.publishEntered <- struct{}{}
	<-f.release
	return nil
}

type fakeRuntimeWorkerService struct {
	calls           []string
	deployRoute     domain.Route
	autoStartRoutes []domain.Route
	deployErr       error
	restartErr      error
	removeErr       error
	syncErr         error
	autoStartErr    error
	listNetworksErr error
	containers      map[string]*domain.Container
	routes          []domain.RouteInfo
	networks        []*domain.NetworkInfo
	volumes         []*domain.VolumeInfo
}

func (f *fakeRuntimeWorkerService) Deploy(_ context.Context, route domain.Route) (*domain.Container, error) {
	f.calls = append(f.calls, "Deploy:"+route.Domain+":"+route.Image)
	f.deployRoute = route
	return &domain.Container{ID: "new"}, f.deployErr
}

func (f *fakeRuntimeWorkerService) Restart(_ context.Context, domainName string, withAttachments bool) error {
	f.calls = append(f.calls, "Restart:"+domainName+":"+boolString(withAttachments))
	return f.restartErr
}

func (f *fakeRuntimeWorkerService) ReconcileRemovedRoute(_ context.Context, domainName string) (*domain.CleanupReport, error) {
	f.calls = append(f.calls, "Remove:"+domainName)
	return &domain.CleanupReport{}, f.removeErr
}

func (f *fakeRuntimeWorkerService) SyncContainers(context.Context) error {
	f.calls = append(f.calls, "Sync")
	return f.syncErr
}

func (f *fakeRuntimeWorkerService) AutoStart(_ context.Context, routes []domain.Route) error {
	count := "0"
	if len(routes) == 1 {
		count = "1"
	}
	f.calls = append(f.calls, "AutoStart:"+count)
	f.autoStartRoutes = routes
	return f.autoStartErr
}

func (f *fakeRuntimeWorkerService) List(context.Context) map[string]*domain.Container {
	return f.containers
}
func (f *fakeRuntimeWorkerService) ListRoutesWithDetails(context.Context) []domain.RouteInfo {
	return f.routes
}
func (f *fakeRuntimeWorkerService) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	return f.networks, f.listNetworksErr
}

func (f *fakeRuntimeWorkerService) ListVolumes(context.Context) ([]*domain.VolumeInfo, error) {
	return f.volumes, nil
}

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
