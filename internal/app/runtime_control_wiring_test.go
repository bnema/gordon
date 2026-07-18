package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRuntimeCommandClientFromEndpoint(t *testing.T) {
	client, err := createRuntimeCommandClient(context.Background(), RuntimeControlConfig{Endpoint: "runtime.example.com:443", Token: "token"})
	assert.NoError(t, err)
	assert.NotNil(t, client)

	client, err = createRuntimeCommandClient(context.Background(), RuntimeControlConfig{})
	assert.NoError(t, err)
	assert.Nil(t, client)
}

func TestCreateRuntimeCommandClientUsesPrivateUnixSocketWithRequiredToken(t *testing.T) {
	client, err := createRuntimeCommandClient(context.Background(), RuntimeControlConfig{
		Endpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock",
		Token:    "component-token",
	})
	require.NoError(t, err)
	require.NotNil(t, client)
	_, err = createRuntimeCommandClient(context.Background(), RuntimeControlConfig{Endpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock"})
	require.Error(t, err, "Unix transport requires a component token")
}

func TestRuntimeControlConfigDefaultsAndMapsInsecureTransportOptIn(t *testing.T) {
	v := viper.New()
	require.NoError(t, loadConfig(v, ""))
	assert.False(t, v.GetBool("runtime.insecure"))

	v.Set("runtime.insecure", true)
	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	assert.True(t, cfg.Runtime.Insecure)
}

func TestRuntimeLogReaderUsesCommandClientForControlRole(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	reader := runtimeLogReaderForServices(&services{role: RoleControl, runtimeCommandClient: client})
	_, ok := reader.(fakeRuntimeCommandClientForApp)
	assert.True(t, ok)
}

func TestRuntimeLogReaderKeepsLocalReaderForMonolith(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	reader := runtimeLogReaderForServices(&services{role: RoleMonolith, runtimeCommandClient: client})
	_, ok := reader.(fakeRuntimeCommandClientForApp)
	assert.False(t, ok)
}

func TestRuntimeVolumeManagerUsesCommandClientForControlRole(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	manager := runtimeVolumeManagerForServices(&services{role: RoleControl, runtimeCommandClient: client})
	_, ok := manager.(fakeRuntimeCommandClientForApp)
	assert.True(t, ok)
}

func TestRuntimeVolumeManagerKeepsLocalManagerForMonolith(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	manager := runtimeVolumeManagerForServices(&services{role: RoleMonolith, runtimeCommandClient: client})
	_, ok := manager.(fakeRuntimeCommandClientForApp)
	assert.False(t, ok)
}

func TestRuntimeImageManagerUsesCommandClientForControlRole(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	manager := runtimeImageManagerForServices(&services{role: RoleControl, runtimeCommandClient: client})
	_, ok := manager.(fakeRuntimeCommandClientForApp)
	assert.True(t, ok)
}

func TestRuntimeImageManagerKeepsLocalManagerForMonolith(t *testing.T) {
	client := fakeRuntimeCommandClientForApp{}
	manager := runtimeImageManagerForServices(&services{role: RoleMonolith, runtimeCommandClient: client})
	_, ok := manager.(fakeRuntimeCommandClientForApp)
	assert.False(t, ok)
}

func TestRuntimeControlFacadeConstructedWhenCommandClientAvailableForControlRole(t *testing.T) {
	svc := &services{role: RoleControl, runtimeCommandClient: fakeRuntimeCommandClientForApp{}}

	initRuntimeControlFacade(svc)

	assert.NotNil(t, svc.runtimeControl)
}

func TestRuntimeControlFacadeNotConstructedWithoutCommandClient(t *testing.T) {
	svc := &services{role: RoleControl}

	initRuntimeControlFacade(svc)

	assert.Nil(t, svc.runtimeControl)
}

func TestRuntimeControlFacadeNotConstructedForMonolithRole(t *testing.T) {
	svc := &services{role: RoleMonolith, runtimeCommandClient: fakeRuntimeCommandClientForApp{}}

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

func (fakeRuntimeCommandClientForApp) ReadRouteLogs(context.Context, string, bool) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func (fakeRuntimeCommandClientForApp) ListRuntimeVolumes(context.Context) ([]*domain.VolumeInfo, error) {
	return nil, nil
}

func (fakeRuntimeCommandClientForApp) RemoveRuntimeVolume(context.Context, string, bool) error {
	return nil
}

func (fakeRuntimeCommandClientForApp) ListRuntimeImages(context.Context) ([]domain.RuntimeImageDetail, error) {
	return nil, nil
}

func (fakeRuntimeCommandClientForApp) PruneRuntimeImages(context.Context, bool) (domain.RuntimePruneResult, error) {
	return domain.RuntimePruneResult{}, nil
}

func (fakeRuntimeCommandClientForApp) ApplyStandaloneService(context.Context, domain.ApplyStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) RemoveStandaloneService(context.Context, domain.RemoveStandaloneServiceCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{}, nil
}

func (fakeRuntimeCommandClientForApp) ListStandaloneServiceState(context.Context) ([]domain.RuntimeStandaloneServiceState, error) {
	return nil, nil
}
