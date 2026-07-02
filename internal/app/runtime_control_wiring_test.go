package app

import (
	"context"
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestCreateRuntimeCommandClientFromEndpoint(t *testing.T) {
	client, err := createRuntimeCommandClient(context.Background(), RuntimeControlConfig{Endpoint: "runtime.example.com:443", Token: "token"})
	assert.NoError(t, err)
	assert.NotNil(t, client)

	client, err = createRuntimeCommandClient(context.Background(), RuntimeControlConfig{})
	assert.NoError(t, err)
	assert.Nil(t, client)
}

func TestRuntimeControlFacadeConstructedWhenCommandClientAvailable(t *testing.T) {
	svc := &services{runtimeCommandClient: fakeRuntimeCommandClientForApp{}}

	initRuntimeControlFacade(svc)

	assert.NotNil(t, svc.runtimeControl)
}

func TestRuntimeControlFacadeNotConstructedWithoutCommandClient(t *testing.T) {
	svc := &services{}

	initRuntimeControlFacade(svc)

	assert.Nil(t, svc.runtimeControl)
}

type fakeRuntimeCommandClientForApp struct{}

func (fakeRuntimeCommandClientForApp) DeployRoute(context.Context, domain.DeployRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) RestartRoute(context.Context, domain.RestartRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) RemoveRoute(context.Context, domain.RemoveRouteCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) Reconcile(context.Context, domain.ReconcileRuntimeCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}
