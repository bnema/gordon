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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Host {
		case "app.example.test":
			assert.Equal(t, "/", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case "registry.example.test":
			assert.Equal(t, "/v2/", r.URL.Path)
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
	assert.NotEmpty(t, runtime.commands)
	for _, command := range runtime.commands {
		assert.Equal(t, domain.RuntimeComponentLifecycleHealth, command.LifecycleAction)
	}
}

func TestMigrationTrafficChecksRejectNonLoopbackProbeEndpoint(t *testing.T) {
	assert.Error(t, validLoopbackProbeEndpoint("example.invalid:8080"))
	assert.Error(t, validLoopbackProbeEndpoint("127.0.0.1"))
	assert.NoError(t, validLoopbackProbeEndpoint("127.0.0.1:8080"))
}
