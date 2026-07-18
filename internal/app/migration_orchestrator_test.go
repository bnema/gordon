package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationServicePrepareRequiresConfiguredCandidateBeforeMutation(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	launcher := &recordingComponentLauncher{}
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	service, err := NewMigrationService(NewMigrationPreflight(passingMigrationProbes(nil)), store)
	require.NoError(t, err)
	service.WithMigrationOrchestrator(orchestrator)
	_, err = service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration"})
	require.Error(t, err)
	assert.Empty(t, launcher.calls)
	require.NoFileExists(t, store.Path())
}

func TestMigrationOrchestratorDryRunAndPrepareAreOrderedIdempotent(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)

	report, err := orchestrator.DryRun(context.Background())
	require.NoError(t, err)
	assert.True(t, report.Ready)
	assert.Empty(t, launcher.calls, "dry-run must not mutate components")

	checkpoint := MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2"}
	prepared, err := orchestrator.Prepare(context.Background(), checkpoint)
	require.NoError(t, err)
	assert.Equal(t, MigrationPhasePrepared, prepared.Phase)
	assert.Equal(t, []string{"gordon-control-fixture-migration-g1", "gordon-runtime-fixture-migration-g1", "gordon-registry-fixture-migration-g1", "gordon-edge-fixture-migration-g1"}, prepared.PreparedComponents)
	assert.Equal(t, []string{"network", "start:control", "start:runtime", "start:registry", "start:edge", "health:control", "health:runtime", "health:registry", "health:edge"}, launcher.calls)
	assert.NotEqual(t, MigrationPhaseSwitched, prepared.Phase)

	launcher.calls = nil
	_, err = orchestrator.Prepare(context.Background(), *prepared)
	require.NoError(t, err)
	assert.Equal(t, []string{"health:control", "health:runtime", "health:registry", "health:edge"}, launcher.calls, "resume must not recreate already checkpointed components")
}

func TestMigrationOrchestratorConnectsEdgeOnlyAfterAllHealthChecks(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	_, err = orchestrator.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", EdgeAppNetworks: []string{"gordon-app-one", "gordon-app-two"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"network", "start:control", "start:runtime", "start:registry", "start:edge", "health:control", "health:runtime", "health:registry", "health:edge", "connect:gordon-app-one", "connect:gordon-app-two"}, launcher.calls)
}

func TestMigrationOrchestratorFailureRetainsOldPathAndCleanupNeverVolumes(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	checkpoint := MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", OldServingPath: "monolith"}
	plan, err := NewComponentLaunchPlan(checkpoint)
	require.NoError(t, err)
	require.NoError(t, orchestrator.CleanupPrepared(context.Background(), plan))
	assert.NotContains(t, launcher.calls, "remove:volume")
	assert.NotEmpty(t, launcher.calls)
}
