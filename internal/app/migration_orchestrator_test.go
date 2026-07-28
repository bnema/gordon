package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

type profileEnforcingHandoffRuntime struct {
	handoffRuntime
}

func (r *profileEnforcingHandoffRuntime) SelfUpdateRuntime(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	r.commands = append(r.commands, command)
	validProfile := command.LifecycleProfile.IsFixedFor(command.TargetComponentRole)
	if domain.IsRuntimeComponentLifecycleReadAction(command.LifecycleAction) {
		validProfile = command.LifecycleProfile.IsFixedIdentityOnlyFor(command.TargetComponentRole)
	}
	if command.LifecycleAction != domain.RuntimeComponentLifecycleEnsureNetwork && !validProfile {
		return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusDenied, Error: &domain.RuntimeCommandError{Message: "component lifecycle process identity is not allowed"}}, nil
	}
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}

func TestMigrationServicePrepareRequiresConfiguredCandidateBeforeMutation(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	launcher := &recordingComponentLauncher{}
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	service, err := NewMigrationService(NewMigrationPreflight(passingMigrationProbes(nil)), store, MigrationEnvOptions{Config: Config{}})
	require.NoError(t, err)
	service.WithMigrationOrchestrator(orchestrator)
	_, err = service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration"})
	require.Error(t, err)
	assert.Empty(t, launcher.calls)
	require.NoFileExists(t, store.Path())
}

func TestMigrationOrchestratorPostHandoffHealthUsesExactRoleIdentities(t *testing.T) {
	plan, err := NewComponentLaunchPlan(MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2"})
	require.NoError(t, err)
	runtimeComponent, ok := componentForRole(plan, domain.ComponentRoleRuntime)
	require.True(t, ok)
	oldRuntime := &handoffRuntime{}
	replacement := &profileEnforcingHandoffRuntime{handoffRuntime: handoffRuntime{
		probe: out.RuntimeEnvironment{APIReachable: true, Rootless: true},
		states: []domain.RuntimeActualStateSnapshot{{
			SourceComponentID: runtimeComponent.ComponentID,
			Containers: []domain.RuntimeContainerState{{
				Name: runtimeComponent.ComponentID, Status: domain.ContainerStatusRunning,
				Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: string(domain.ComponentRoleRuntime), domain.LabelComponentGeneration: "1"},
			}},
		}},
	}}
	launcher, err := NewRuntimeComponentLauncherWithHandoff(oldRuntime, func(context.Context, ComponentLaunchComponent) (RuntimeHandoffClient, error) { return replacement, nil })
	require.NoError(t, err)
	require.NoError(t, launcher.TransferRuntimeCommandChannel(t.Context(), runtimeComponent))
	orchestrator := &MigrationOrchestrator{launcher: launcher}
	require.NoError(t, orchestrator.checkPlanHealth(t.Context(), plan))
	require.Len(t, replacement.commands, 4)
	for index, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry, domain.ComponentRoleEdge} {
		command := replacement.commands[index]
		expected, identityOK := domain.FixedComponentProcessIdentity(role)
		require.True(t, identityOK)
		assert.Equal(t, domain.RuntimeComponentLifecycleHealth, command.LifecycleAction)
		assert.Equal(t, role, command.TargetComponentRole)
		assert.Equal(t, domain.RuntimeComponentLifecycleProfile{ProcessIdentity: expected}, command.LifecycleProfile)
		assert.True(t, command.HasOnlyReadLifecycleIdentity())
	}
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
	assert.True(t, prepared.PrepareComplete, "switch must have a durable completed-prepare barrier")
	assert.Equal(t, []string{"gordon-control-fixture-migration-g1", "gordon-runtime-fixture-migration-g1", "gordon-registry-fixture-migration-g1", "gordon-edge-fixture-migration-g1"}, prepared.PreparedComponents)
	assert.Equal(t, []string{"network", "start:control", "start:runtime", "start:registry", "start:edge", "health:control", "health:runtime", "health:registry", "health:edge"}, launcher.calls)
	assert.NotEqual(t, MigrationPhaseSwitched, prepared.Phase)

	launcher.calls = nil
	_, err = orchestrator.Prepare(context.Background(), *prepared)
	require.NoError(t, err)
	assert.Equal(t, []string{"health:control", "health:runtime", "health:registry", "health:edge"}, launcher.calls, "resume must not recreate already checkpointed components")
}

func TestMigrationOrchestratorFailsClosedWhenManagedRoutesHaveNoUnambiguousNetwork(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	orchestrator.WithRuntimeSnapshotAppNetworks(migrationRouteStateSubscriber{snapshot: domain.RuntimeActualStateSnapshot{
		Routes: []domain.RuntimeRouteState{{Domain: "app.example.test", Status: domain.RouteTargetStatusUnavailable}},
	}})

	_, err = orchestrator.Prepare(t.Context(), MigrationCheckpoint{MigrationID: "fixture-migration", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "managed runtime routes have no unambiguous app networks")
	assert.Empty(t, launcher.calls, "an ambiguous runtime route must not start an edge disconnected from its backend")
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

type migrationRouteStateSubscriber struct {
	snapshot domain.RuntimeActualStateSnapshot
}

func (s migrationRouteStateSubscriber) SubscribeRuntimeState(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	updates := make(chan domain.RuntimeActualStateSnapshot, 1)
	updates <- s.snapshot
	close(updates)
	return updates, nil
}

func TestMigrationOrchestratorReconnectsEdgeAppNetworksDespiteCheckpoint(t *testing.T) {
	launcher := &recordingComponentLauncher{}
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	checkpoint := MigrationCheckpoint{
		MigrationID: "fixture-migration", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2", StartedAt: time.Now().UTC(),
		EdgeAppNetworks: []string{"gordon-app-one", "gordon-app-two"}, ConnectedEdgeNetworks: []string{"gordon-app-one"},
		PreparedComponents: []string{"gordon-control-fixture-migration-g1", "gordon-runtime-fixture-migration-g1", "gordon-registry-fixture-migration-g1", "gordon-edge-fixture-migration-g1"},
	}
	_, err = orchestrator.Prepare(context.Background(), checkpoint)
	require.NoError(t, err)
	assert.Equal(t, []string{"health:control", "health:runtime", "health:registry", "health:edge", "connect:gordon-app-one", "connect:gordon-app-two"}, launcher.calls)
	persisted, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"gordon-app-one", "gordon-app-two"}, persisted.ConnectedEdgeNetworks)
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
