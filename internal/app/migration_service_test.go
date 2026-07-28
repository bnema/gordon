package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupFailingLauncher records lifecycle intents like recordingComponentLauncher
// but fails the reverse-order removal so cleanup errors can be asserted to leave
// the durable checkpoint intact.
type cleanupFailingLauncher struct {
	*recordingComponentLauncher
}

func (l *cleanupFailingLauncher) RemovePreparedComponent(_ context.Context, component ComponentLaunchComponent) error {
	l.calls = append(l.calls, "remove:"+string(component.Role))
	return fmt.Errorf("runtime refused component removal")
}

func preparedCleanupCheckpoint() MigrationCheckpoint {
	return MigrationCheckpoint{MigrationID: "fixture-migration", StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", OldServingPath: "monolith"}
}

func newCleanupMigrationService(t *testing.T, launcher ComponentLauncher) (*MigrationService, *MigrationCheckpointStore) {
	t.Helper()
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	service, err := NewMigrationService(NewMigrationPreflight(passingMigrationProbes(nil)), store, MigrationEnvOptions{
		Config: Config{}, Environment: map[string]string{}, Directory: filepath.Join(filepath.Dir(store.Path()), "env"),
	})
	require.NoError(t, err)
	service.WithMigrationOrchestrator(orchestrator)
	return service, store
}

func TestMigrationCleanupRemovesPreparedComponentsInReverseAndClearsCheckpoint(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	service, store := newCleanupMigrationService(t, launcher)
	require.NoError(t, store.Save(preparedCleanupCheckpoint()))

	require.NoError(t, service.MigrationCleanup(context.Background()))
	assert.Equal(t, []string{"remove:edge", "remove:registry", "remove:runtime", "remove:control"}, launcher.calls)
	assert.NoFileExists(t, store.Path())
}

func TestMigrationCleanupRefusesAfterSwitch(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	service, store := newCleanupMigrationService(t, launcher)
	checkpoint := preparedCleanupCheckpoint()
	checkpoint.Phase = MigrationPhaseSwitched
	require.NoError(t, store.Save(checkpoint))

	err := service.MigrationCleanup(context.Background())
	require.Error(t, err)
	assert.Empty(t, launcher.calls, "cleanup must never touch components that are serving traffic")
	assert.FileExists(t, store.Path())
}

func TestMigrationCleanupRefusesAfterRuntimeHandoff(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	service, store := newCleanupMigrationService(t, launcher)
	checkpoint := preparedCleanupCheckpoint()
	checkpoint.RuntimeChannelTransferred = true
	require.NoError(t, store.Save(checkpoint))

	err := service.MigrationCleanup(context.Background())
	require.Error(t, err)
	assert.Empty(t, launcher.calls, "cleanup through the old authority is wrong after handoff")
	assert.FileExists(t, store.Path())
}

func TestMigrationCleanupLeavesCheckpointWhenOrchestratorFails(t *testing.T) {
	launcher := &cleanupFailingLauncher{recordingComponentLauncher: &recordingComponentLauncher{}}
	service, store := newCleanupMigrationService(t, launcher)
	require.NoError(t, store.Save(preparedCleanupCheckpoint()))

	err := service.MigrationCleanup(context.Background())
	require.Error(t, err)
	assert.FileExists(t, store.Path(), "a failed removal must not discard the recovery checkpoint")
}

func TestMigrationCleanupWithoutCheckpointIsNotReady(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	service, store := newCleanupMigrationService(t, launcher)

	err := service.MigrationCleanup(context.Background())
	require.ErrorIs(t, err, ErrMigrationNotReady)
	assert.Empty(t, launcher.calls)
	assert.NoFileExists(t, store.Path())
}

func TestSwitchRebuildsBootstrapRuntimeEndpointsBeforePrepare(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataDir, "migration", "fixture-migration"), 0o700))
	store, err := NewMigrationCheckpointStore(filepath.Join(dataDir, "checkpoint.json"))
	require.NoError(t, err)
	launcher := &recordingComponentLauncher{}
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	runtime := &recordingTrafficRuntime{}
	switcher, err := NewTrafficSwitch(runtime, fixtureTrafficChecks{})
	require.NoError(t, err)
	orchestrator.WithTrafficSwitcher(switcher)
	cfg := Config{}
	cfg.Server.DataDir = dataDir
	service, err := NewMigrationService(NewMigrationPreflight(passingMigrationProbes(nil)), store, MigrationEnvOptions{Config: cfg, Environment: map[string]string{}, Directory: filepath.Join(dataDir, "migration", "env")})
	require.NoError(t, err)
	service.WithMigrationOrchestrator(orchestrator)
	checkpoint := MigrationCheckpoint{
		MigrationID: "fixture-migration", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2",
		StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, RuntimeChannelTransferred: true, PrepareComplete: false, OldServingPath: "monolith",
		PreparedComponents:       []string{"gordon-control-fixture-migration-g1", "gordon-runtime-fixture-migration-g1"},
		BootstrapRuntimeEndpoint: "unix:///var/lib/gordon/migration/fixture-migration/runtime-control.sock",
		RouteSnapshotGeneration:  7,
		EdgeAppNetworks:          []string{"gordon-app-fixture"},
		PublicPortBindings:       []MigrationPortBinding{{Role: "edge", HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 8080, Protocol: "tcp"}, {Role: "edge", HostIP: "127.0.0.1", HostPort: 5000, ContainerPort: 5000, Protocol: "tcp"}},
	}
	require.NoError(t, store.Save(checkpoint))

	switched, err := service.Switch(context.Background())
	require.NoError(t, err)
	assert.Equal(t, MigrationPhaseSwitched, switched.Phase)
	assert.Contains(t, launcher.calls, "start:registry")
	assert.Contains(t, launcher.calls, "connect:gordon-app-fixture")
	require.Len(t, runtime.commands, 1)
	assert.Equal(t, "monolith", runtime.commands[0].OldServingComponentID)
}
