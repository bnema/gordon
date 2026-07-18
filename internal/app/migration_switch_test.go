package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fixtureTrafficSwitcher struct {
	calls int
	err   error
}

func (s *fixtureTrafficSwitcher) Switch(context.Context, MigrationCheckpoint, ComponentLaunchPlan) error {
	s.calls++
	return s.err
}

func TestMigrationOrchestratorSwitchCheckpointsRetryAndRetainsOldPath(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	switcher := &fixtureTrafficSwitcher{err: errors.New("edge test failed")}
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, &recordingComponentLauncher{})
	require.NoError(t, err)
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", StartedAt: orchestrator.now(), Phase: MigrationPhasePrepared, RouteSnapshotGeneration: 1, OldServingPath: "monolith"}
	_, err = orchestrator.WithTrafficSwitcher(switcher).Switch(context.Background(), checkpoint)
	require.Error(t, err)
	persisted, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, MigrationPhasePrepared, persisted.Phase)
	assert.Equal(t, uint64(1), persisted.SwitchAttempts)
	assert.Equal(t, "switch", persisted.LastRetryPhase)
	assert.Equal(t, "monolith", persisted.OldServingPath)

	switcher.err = nil
	switched, err := orchestrator.Switch(context.Background(), *persisted)
	require.NoError(t, err)
	assert.Equal(t, MigrationPhaseSwitched, switched.Phase)
	assert.Empty(t, switched.LastRetryPhase)
	assert.Equal(t, 2, switcher.calls)
}
