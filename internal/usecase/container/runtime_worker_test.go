package container

import (
	"context"
	"errors"
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

	fake = &fakeRuntimeWorkerService{restartErr: context.Canceled}
	worker = NewRuntimeWorker(fake)
	canceled, err := worker.RestartRoute(context.Background(), domain.RestartRouteCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("cmd-cancel"), Domain: "app.example.com"})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusDenied, canceled.Status)
	require.NotNil(t, canceled.Error)
	assert.Equal(t, "context_canceled", canceled.Error.Code)
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
			"c1": {ID: "c1", Name: "gordon-app.example.com", Image: "app:latest", ImageID: "sha256:abc", Status: "running", Ports: []int{8080}, Created: time.Unix(10, 0), Labels: map[string]string{domain.LabelDomain: "app.example.com", domain.LabelManaged: "true", domain.LabelProxyPort: "8080", "com.example.secret": "do-not-leak"}},
			"c2": {ID: "c2", Name: "gordon-down.example.com", Image: "down:latest", Status: "exited", Labels: map[string]string{domain.LabelDomain: "down.example.com", "TOKEN": "secret"}},
		},
		routes: []domain.RouteInfo{
			{Domain: "app.example.com", ContainerID: "c1", ContainerStatus: "running", Network: "gordon-net"},
			{Domain: "down.example.com", ContainerID: "c2", ContainerStatus: "exited", Network: "gordon-net"},
		},
		networks: []*domain.NetworkInfo{{Name: "gordon-net", Driver: "bridge", Containers: []string{"gordon-target-app-example-com", "localhost", "127.0.0.1"}}},
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
	assert.NotContains(t, snapshot.Routes[0].EdgeTargetAlias, "localhost")
	assert.Equal(t, domain.RouteTargetStatusUnavailable, snapshot.Routes[1].Status)
	assert.Empty(t, snapshot.Routes[1].EdgeTargetAlias)
	require.Len(t, snapshot.Containers, 2)
	assert.NotContains(t, snapshot.Containers[0].Labels, "com.example.secret")
	assert.NotContains(t, snapshot.Containers[1].Labels, "TOKEN")
	require.Len(t, snapshot.Networks, 1)
	assert.Equal(t, []string{"gordon-target-app-example-com"}, snapshot.Networks[0].Aliases)
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

func boolString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
