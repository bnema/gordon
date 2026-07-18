package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	edgesnapshotusecase "github.com/bnema/gordon/internal/usecase/edgesnapshot"
)

type trafficCheckRuntime struct {
	commands []domain.RuntimeSelfUpdateCommand
}

func (r *trafficCheckRuntime) SelfUpdateRuntime(_ context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	r.commands = append(r.commands, command)
	return domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil
}

type trafficCheckState struct {
	snapshot domain.RuntimeActualStateSnapshot
}

func (s trafficCheckState) SubscribeRuntimeState(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	updates := make(chan domain.RuntimeActualStateSnapshot, 1)
	updates <- s.snapshot
	close(updates)
	return updates, nil
}

var _ out.RuntimeStateSubscriber = trafficCheckState{}

func TestMigrationTrafficChecksUseLifecycleAndLoopbackProbes(t *testing.T) {
	seed := "private-runtime-handoff-token"
	oldRegistryCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "app.example.test":
			assert.Equal(t, "/", r.URL.Path)
			assert.Equal(t, migrationProbeToken(seed), r.Header.Get(migrationProbeHeader))
			w.WriteHeader(http.StatusNoContent)
		case "registry.example.test":
			assert.Equal(t, "/v2/", r.URL.Path)
			if r.Header.Get(migrationProbeHeader) == "" {
				oldRegistryCalls++
			} else {
				assert.Equal(t, migrationProbeToken(seed), r.Header.Get(migrationProbeHeader))
			}
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.Error(w, "unexpected host", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	endpoint := server.Listener.Addr().String()
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, RouteSnapshotGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, OldServingPath: "old-monolith", BootstrapEdgeProbeEndpoint: endpoint, OldServingProbeEndpoint: endpoint}
	require.NoError(t, store.Save(checkpoint))
	plan, err := NewComponentLaunchPlan(checkpoint)
	require.NoError(t, err)
	edge, ok := componentForRole(plan, domain.ComponentRoleEdge)
	require.True(t, ok)
	tracker := edgesnapshotusecase.NewAppliedStateTrackerAny()
	require.NoError(t, tracker.ReportAuthenticatedAppliedState(context.Background(), edge.ComponentID, edgesnapshotusecase.AppliedState{ComponentID: edge.ComponentID, RouteGeneration: 1, TrafficGeneration: 1, Healthy: true}))
	runtime := &trafficCheckRuntime{}
	state := trafficCheckState{snapshot: domain.RuntimeActualStateSnapshot{Generation: 1, StateVersion: "fixture", SourceComponentID: "runtime", ObservedAt: time.Now(), Containers: []domain.RuntimeContainerState{{Name: "old-monolith", Status: domain.ContainerStatusRunning, Labels: map[string]string{domain.LabelManaged: "true"}}}}}
	var cfg Config
	cfg.Runtime.Token = seed
	cfg.Server.GordonDomain, cfg.Server.RegistryDomain = "app.example.test", "registry.example.test"
	checks, err := newMigrationTrafficChecks(runtime, state, store, tracker, cfg)
	require.NoError(t, err)
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry, domain.ComponentRoleEdge} {
		require.NoError(t, checks.ComponentHealthy(context.Background(), role))
		require.NoError(t, checks.ComponentAuthenticationHealthy(context.Background(), role))
	}
	require.NoError(t, checks.TestApplicationThroughEdge(context.Background()))
	require.NoError(t, checks.TestRegistryV2ThroughEdge(context.Background()))
	require.NoError(t, checks.OldServingPathHealthy(context.Background(), "old-monolith"))
	assert.Equal(t, 1, oldRegistryCalls, "old serving path must be a normal monolith registry request, not a prepared-edge credential probe")
	assert.NotEmpty(t, runtime.commands)
	for _, command := range runtime.commands {
		assert.Equal(t, domain.RuntimeComponentLifecycleHealth, command.LifecycleAction)
	}
}

func TestMigrationTrafficChecksOldServingFailsClosedWithoutMonolithRegistryEndpoint(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, RouteSnapshotGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, OldServingPath: "old-monolith"}
	require.NoError(t, store.Save(checkpoint))
	runtime := &trafficCheckRuntime{}
	state := trafficCheckState{snapshot: domain.RuntimeActualStateSnapshot{Generation: 1, StateVersion: "fixture", SourceComponentID: "runtime", ObservedAt: time.Now(), Containers: []domain.RuntimeContainerState{{Name: "old-monolith", Status: domain.ContainerStatusRunning, Labels: map[string]string{domain.LabelManaged: "true"}}}}}
	checks, err := newMigrationTrafficChecks(runtime, state, store, edgesnapshotusecase.NewAppliedStateTrackerAny(), Config{})
	require.NoError(t, err)

	require.Error(t, checks.OldServingPathHealthy(context.Background(), "old-monolith"))
	assert.Empty(t, runtime.commands, "an unproven old serving path must not send lifecycle commands")
}

func TestMigrationAppliedStatePersistsOnlyAuthenticatedCurrentEdgeGeneration(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, OldServingPath: "old-monolith"}
	require.NoError(t, store.Save(checkpoint))
	plan, err := NewComponentLaunchPlan(checkpoint)
	require.NoError(t, err)
	edge, ok := componentForRole(plan, domain.ComponentRoleEdge)
	require.True(t, ok)

	receiver, err := newMigrationAppliedStateReceiver(store, edgesnapshotusecase.NewAppliedStateTrackerAny())
	require.NoError(t, err)
	wrong := edgesnapshotusecase.AppliedState{ComponentID: "other-edge", RouteGeneration: 4, TrafficGeneration: 4, Healthy: true}
	assert.Error(t, receiver.ReportAuthenticatedAppliedState(context.Background(), "other-edge", wrong))
	require.NoError(t, receiver.ReportAuthenticatedAppliedState(context.Background(), edge.ComponentID, edgesnapshotusecase.AppliedState{ComponentID: edge.ComponentID, RouteGeneration: 4, TrafficGeneration: 4, Healthy: true}))

	persisted, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, uint64(4), persisted.RouteSnapshotGeneration)
	assert.Equal(t, edge.ComponentID, persisted.AppliedEdgeComponentID)
	// An equal acknowledgement is a reconnect-safe retry, while a lower
	// generation is rejected even by a fresh process-local tracker.
	require.NoError(t, receiver.ReportAuthenticatedAppliedState(context.Background(), edge.ComponentID, edgesnapshotusecase.AppliedState{ComponentID: edge.ComponentID, RouteGeneration: 4, TrafficGeneration: 4, Healthy: true}))
	assert.Error(t, receiver.ReportAuthenticatedAppliedState(context.Background(), edge.ComponentID, edgesnapshotusecase.AppliedState{ComponentID: edge.ComponentID, RouteGeneration: 3, TrafficGeneration: 3, Healthy: true}))
	restarted, err := newMigrationAppliedStateReceiver(store, edgesnapshotusecase.NewAppliedStateTrackerAny())
	require.NoError(t, err)
	assert.Error(t, restarted.ReportAuthenticatedAppliedState(context.Background(), edge.ComponentID, edgesnapshotusecase.AppliedState{ComponentID: edge.ComponentID, RouteGeneration: 3, TrafficGeneration: 3, Healthy: true}))

	// The persisted attestation is restart-safe: a fresh tracker can still use
	// it to gate the old monolith's separately invoked switch command.
	checks, err := newMigrationTrafficChecks(&trafficCheckRuntime{}, trafficCheckState{}, store, edgesnapshotusecase.NewAppliedStateTrackerAny(), Config{})
	require.NoError(t, err)
	generation, err := checks.AppliedRouteGeneration(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(4), generation)
}

func TestMigrationTrafficChecksRejectNonLoopbackProbeEndpoint(t *testing.T) {
	assert.Error(t, validLoopbackProbeEndpoint("example.invalid:8080"))
	assert.Error(t, validLoopbackProbeEndpoint("127.0.0.1"))
	assert.NoError(t, validLoopbackProbeEndpoint("127.0.0.1:8080"))
}
