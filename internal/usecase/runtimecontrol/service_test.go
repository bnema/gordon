package runtimecontrol

import (
	"context"
	"testing"
	"time"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceDeployRestartRemoveCommands(t *testing.T) {
	client := &fakeRuntimeCommandClient{}
	svc := NewService(nil, client, "control-1")
	svc.now = func() time.Time { return time.Unix(10, 0).UTC() }
	ctx := context.Background()

	deploy, err := svc.DeployRoute(ctx, domain.Route{Domain: "app.example.com", Image: "app:latest", Env: []string{"A=1"}})
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, deploy.Status)
	assert.Equal(t, "app.example.com", client.deploy.Domain)
	assert.Equal(t, "app:latest", client.deploy.Image)
	assert.Equal(t, []string{"A=1"}, client.deploy.Env)
	assert.True(t, client.deploy.InternalDeploy)
	assert.Equal(t, "control-1", client.deploy.SourceComponentID)

	_, err = svc.RestartRoute(ctx, "app.example.com", true)
	require.NoError(t, err)
	assert.Equal(t, "app.example.com", client.restart.Domain)
	assert.True(t, client.restart.WithAttachments)

	_, err = svc.RemoveRoute(ctx, "app.example.com", true)
	require.NoError(t, err)
	assert.Equal(t, "app.example.com", client.remove.Domain)
	assert.True(t, client.remove.Force)
}

func TestServiceReconcileConfiguredRoutes(t *testing.T) {
	ctx := context.Background()
	configSvc := inmocks.NewMockConfigService(t)
	routes := []domain.Route{{Domain: "app.example.com", Image: "app:latest"}}
	configSvc.EXPECT().GetRoutes(ctx).Return(routes)
	client := &fakeRuntimeCommandClient{}
	svc := NewService(configSvc, client, "control-1")
	svc.now = func() time.Time { return time.Unix(20, 0).UTC() }

	result, err := svc.ReconcileConfiguredRoutes(ctx, "startup")

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	assert.Equal(t, "startup", client.reconcile.Reason)
	assert.Equal(t, 1, client.reconcile.ExpectedRouteCount)
	assert.Equal(t, routes, client.reconcile.DesiredRoutes)
}

func TestServiceUnavailableDependencies(t *testing.T) {
	svc := NewService(nil, nil, "control-1")
	_, err := svc.DeployRoute(context.Background(), domain.Route{Domain: "app.example.com", Image: "app:latest"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runtime command client unavailable")

	svc = NewService(nil, &fakeRuntimeCommandClient{}, "control-1")
	_, err = svc.ReconcileConfiguredRoutes(context.Background(), "startup")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config service unavailable")
}

type fakeRuntimeCommandClient struct {
	deploy    domain.DeployRouteCommand
	restart   domain.RestartRouteCommand
	remove    domain.RemoveRouteCommand
	reconcile domain.ReconcileRuntimeCommand
}

func (f *fakeRuntimeCommandClient) DeployRoute(_ context.Context, command domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	f.deploy = command
	return resultFor(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeCommandClient) RestartRoute(_ context.Context, command domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	f.restart = command
	return resultFor(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeCommandClient) RemoveRoute(_ context.Context, command domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	f.remove = command
	return resultFor(command.RuntimeCommandIdentity), nil
}

func (f *fakeRuntimeCommandClient) Reconcile(_ context.Context, command domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	f.reconcile = command
	return resultFor(command.RuntimeCommandIdentity), nil
}

func resultFor(identity domain.RuntimeCommandIdentity) domain.RuntimeCommandResult {
	return domain.RuntimeCommandResult{CommandID: identity.ID, IdempotencyKey: identity.IdempotencyKey, Generation: identity.Generation, Status: domain.RuntimeCommandStatusSucceeded, StartedAt: identity.RequestedAt, CompletedAt: identity.RequestedAt}
}
