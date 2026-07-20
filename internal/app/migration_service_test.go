package app

import (
	"context"
	"fmt"
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
	service, err := NewMigrationService(NewMigrationPreflight(passingMigrationProbes(nil)), store)
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
