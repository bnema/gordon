package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

type recoveryRuntime struct{}

func (recoveryRuntime) SelfUpdateRuntime(context.Context, domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}
func (recoveryRuntime) ProbeRuntimeEnvironment(context.Context) (out.RuntimeEnvironment, error) {
	return out.RuntimeEnvironment{APIReachable: true, Rootless: true}, nil
}
func (recoveryRuntime) PingRuntime(context.Context) error              { return nil }
func (recoveryRuntime) RuntimeVersion(context.Context) (string, error) { return "fixture", nil }
func (recoveryRuntime) SubscribeRuntimeState(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	return make(chan domain.RuntimeActualStateSnapshot), nil
}

func TestPostHandoffRecoveryTranslatesOnlyValidatedRuntimeSocketAndKeepsTokenOutOfErrors(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewMigrationCheckpointStore(migrationCheckpointPath(dataDir))
	require.NoError(t, err)
	checkpoint := MigrationCheckpoint{
		MigrationID: "fixture", ComponentGeneration: 1, TargetImage: "example.invalid/gordon:v2", StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared,
		RuntimeChannelTransferred: true, BootstrapRuntimeEndpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock",
	}
	require.NoError(t, store.Save(checkpoint))

	secret := "post-handoff-recovery-secret"
	cfg := Config{}
	cfg.Server.DataDir = dataDir
	cfg.Runtime.TokenEnv = "GORDON_COMPONENT_RUNTIME_TOKEN"
	t.Setenv(cfg.Runtime.TokenEnv, secret)
	var got RuntimeControlConfig
	recovery, err := newPostHandoffMigrationRecovery(cfg, store, func(_ context.Context, target RuntimeControlConfig) (RuntimeHandoffClient, error) {
		got = target
		return recoveryRuntime{}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, recovery)
	assert.Equal(t, "unix://"+filepath.Join(dataDir, "migration", "fixture", bootstrapRuntimeSocketName), got.Endpoint)
	assert.Equal(t, cfg.Runtime.TokenEnv, got.TokenEnv)
	assert.Empty(t, got.Token)

	checkpoint.BootstrapRuntimeEndpoint = "unix:///var/run/docker.sock"
	_, err = validatePostHandoffRuntimeEndpoint(checkpoint, dataDir)
	assert.Error(t, err)
}

func TestPostHandoffRecoveryDoesNotLeakRuntimeTokenInDialError(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewMigrationCheckpointStore(migrationCheckpointPath(dataDir))
	require.NoError(t, err)
	require.NoError(t, store.Save(MigrationCheckpoint{
		MigrationID: "fixture", ComponentGeneration: 1, TargetImage: "example.invalid/gordon:v2", StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared,
		RuntimeChannelTransferred: true, BootstrapRuntimeEndpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock",
	}))
	const secret = "post-handoff-recovery-secret"
	cfg := Config{}
	cfg.Server.DataDir = dataDir
	cfg.Runtime.Token = secret
	_, err = newPostHandoffMigrationRecovery(cfg, store, func(context.Context, RuntimeControlConfig) (RuntimeHandoffClient, error) {
		return nil, fmt.Errorf("dial refused token=%s", secret)
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.False(t, strings.Contains(err.Error(), "token="))
}
