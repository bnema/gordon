package app

import (
	"context"
	"fmt"
	"os"
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

type closingRecoveryRuntime struct {
	recoveryRuntime
	closed bool
}

func (r *closingRecoveryRuntime) Close() error {
	r.closed = true
	return nil
}

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

func TestPostHandoffRecoveryUsesCheckpointedRuntimeCredential(t *testing.T) {
	dataDir := t.TempDir()
	checkpoint := recoveryCheckpoint()
	runtimeEnv := writeRecoveryRuntimeEnv(t, dataDir, checkpoint.MigrationID, checkpoint.ComponentGeneration, "GORDON_COMPONENT_RUNTIME_TOKEN=generated-replacement-runtime-token\nGORDON_AUTH_TOKEN_SECRET=runtime-only-secret\n")
	checkpoint.EnvFileReferences = []string{
		filepath.Join(dataDir, "migration", "env", checkpoint.MigrationID, "1", "edge.env"),
		runtimeEnv,
		filepath.Join(dataDir, "migration", "env", checkpoint.MigrationID, "1", "registry.env"),
	}
	store, err := NewMigrationCheckpointStore(migrationCheckpointPath(dataDir))
	require.NoError(t, err)
	require.NoError(t, store.Save(checkpoint))

	cfg := Config{}
	cfg.Server.DataDir = dataDir
	cfg.Runtime.Token = "source-runtime-token-must-not-authenticate-recovery"
	var got RuntimeControlConfig
	runtime := &closingRecoveryRuntime{}
	recovery, err := newPostHandoffMigrationRecovery(cfg, store, func(_ context.Context, target RuntimeControlConfig) (RuntimeHandoffClient, error) {
		got = target
		return runtime, nil
	})
	require.NoError(t, err)
	require.NotNil(t, recovery)
	t.Cleanup(func() { require.NoError(t, recovery.Close()) })
	assert.Equal(t, "unix://"+filepath.Join(dataDir, "migration", "fixture", bootstrapRuntimeSocketName), got.Endpoint)
	assert.Equal(t, "generated-replacement-runtime-token", got.Token)
	assert.Empty(t, got.TokenEnv)
	assert.NotEqual(t, cfg.Runtime.Token, got.Token)
	require.NoError(t, recovery.Close())
	assert.True(t, runtime.closed, "post-handoff recovery must close its explicitly owned runtime client")

	checkpoint.BootstrapRuntimeEndpoint = "unix:///var/run/docker.sock"
	_, err = validatePostHandoffRuntimeEndpoint(checkpoint, dataDir)
	assert.Error(t, err)
}

func TestPostHandoffRecoveryRuntimeEnvValidationFailsClosed(t *testing.T) {
	cases := []struct {
		name       string
		references func(string, string) []string
		prepare    func(*testing.T, string)
	}{
		{name: "missing runtime reference", references: func(string, string) []string { return nil }},
		{name: "ambiguous runtime reference", references: func(expected, _ string) []string { return []string{expected, expected} }},
		{name: "unclean traversal reference", references: func(_ string, root string) []string {
			return []string{root + "/migration/env/fixture/1/../1/runtime.env"}
		}},
		{name: "wrong generation reference", references: func(_ string, root string) []string {
			return []string{filepath.Join(root, "migration", "env", "fixture", "2", "runtime.env")}
		}},
		{name: "symlink runtime environment", references: func(expected, _ string) []string { return []string{expected} }, prepare: func(t *testing.T, expected string) {
			require.NoError(t, os.Remove(expected))
			require.NoError(t, os.Symlink(filepath.Join(filepath.Dir(expected), "outside.env"), expected))
		}},
		{name: "wrong runtime environment mode", references: func(expected, _ string) []string { return []string{expected} }, prepare: func(t *testing.T, expected string) {
			require.NoError(t, os.Chmod(expected, 0o640))
		}},
		{name: "oversized runtime environment", references: func(expected, _ string) []string { return []string{expected} }, prepare: func(t *testing.T, expected string) {
			require.NoError(t, os.WriteFile(expected, []byte(strings.Repeat("x", int(maxRecoveryRuntimeEnvBytes+1))), 0o600))
		}},
		{name: "missing runtime credential", references: func(expected, _ string) []string { return []string{expected} }, prepare: func(t *testing.T, expected string) {
			require.NoError(t, os.WriteFile(expected, []byte("GORDON_COMPONENT_EDGE_TOKEN=edge-secret-must-not-be-read\n"), 0o600))
		}},
		{name: "duplicate runtime credential", references: func(expected, _ string) []string { return []string{expected} }, prepare: func(t *testing.T, expected string) {
			require.NoError(t, os.WriteFile(expected, []byte("GORDON_COMPONENT_RUNTIME_TOKEN=one\nGORDON_COMPONENT_RUNTIME_TOKEN=two\n"), 0o600))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseDataDir := t.TempDir()
			caseCheckpoint := recoveryCheckpoint()
			caseExpected := writeRecoveryRuntimeEnv(t, caseDataDir, caseCheckpoint.MigrationID, caseCheckpoint.ComponentGeneration, "GORDON_COMPONENT_RUNTIME_TOKEN=generated-replacement-runtime-token\n")
			caseCheckpoint.EnvFileReferences = tc.references(caseExpected, caseDataDir)
			if tc.prepare != nil {
				tc.prepare(t, caseExpected)
			}
			_, err := loadPostHandoffRuntimeToken(caseCheckpoint, caseDataDir)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "edge-secret-must-not-be-read")
		})
	}
}

func TestResumePostHandoffTable(t *testing.T) {
	cases := []struct {
		name       string
		checkpoint MigrationCheckpoint
		wantErr    bool
	}{
		{name: "already switched", checkpoint: MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhaseSwitched, RuntimeChannelTransferred: true}, wantErr: false},
		{name: "missing handoff", checkpoint: MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared}, wantErr: true},
		{name: "planned phase", checkpoint: MigrationCheckpoint{MigrationID: "fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePlanned, RuntimeChannelTransferred: true}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			store, err := NewMigrationCheckpointStore(filepath.Join(dataDir, "checkpoint.json"))
			require.NoError(t, err)
			runtime := &recordingTrafficRuntime{}
			switcher, err := NewTrafficSwitch(runtime, fixtureTrafficChecks{})
			require.NoError(t, err)
			orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, &recordingComponentLauncher{})
			require.NoError(t, err)
			orchestrator.WithTrafficSwitcher(switcher)
			service, err := NewMigrationService(NewMigrationPreflight(passingMigrationProbes(nil)), store, MigrationEnvOptions{Config: Config{}})
			require.NoError(t, err)
			service.WithMigrationOrchestrator(orchestrator)
			require.NoError(t, store.Save(tc.checkpoint))
			result, err := service.ResumePostHandoff(context.Background())
			if tc.wantErr {
				require.ErrorIs(t, err, ErrMigrationNotReady)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, MigrationPhaseSwitched, result.Phase)
		})
	}
}

func TestResumePostHandoffComposedCutover(t *testing.T) {
	dataDir := t.TempDir()
	store, err := NewMigrationCheckpointStore(filepath.Join(dataDir, "checkpoint.json"))
	require.NoError(t, err)
	launcher := &recordingComponentLauncher{}
	runtime := &recordingTrafficRuntime{}
	switcher, err := NewTrafficSwitch(runtime, fixtureTrafficChecks{})
	require.NoError(t, err)
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(passingMigrationProbes(nil)), store, launcher)
	require.NoError(t, err)
	orchestrator.WithTrafficSwitcher(switcher)
	service, err := NewMigrationService(NewMigrationPreflight(passingMigrationProbes(nil)), store, MigrationEnvOptions{Config: Config{}})
	require.NoError(t, err)
	service.WithMigrationOrchestrator(orchestrator)
	checkpoint := MigrationCheckpoint{
		MigrationID: "fixture", ComponentGeneration: 1, TargetVersion: "v2", TargetImage: "example.invalid/gordon:v2",
		StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, RuntimeChannelTransferred: true, PrepareComplete: true, OldServingPath: "monolith",
		RouteSnapshotGeneration: 7, EdgeAppNetworks: []string{"gordon-app-fixture"},
		PublicPortBindings: []MigrationPortBinding{{Role: "edge", HostIP: "127.0.0.1", HostPort: 8080, ContainerPort: 8080, Protocol: "tcp"}, {Role: "edge", HostIP: "127.0.0.1", HostPort: 5000, ContainerPort: 5000, Protocol: "tcp"}},
	}
	require.NoError(t, store.Save(checkpoint))

	switched, err := service.ResumePostHandoff(context.Background())
	require.NoError(t, err)
	assert.Equal(t, MigrationPhaseSwitched, switched.Phase)
	require.Len(t, runtime.commands, 1)
	assert.Equal(t, domain.RuntimeComponentLifecycleActivate, runtime.commands[0].LifecycleAction)
	assert.Empty(t, launcher.calls, "post-handoff resume must not recreate components through prepare")

	again, err := service.ResumePostHandoff(context.Background())
	require.NoError(t, err)
	assert.Equal(t, MigrationPhaseSwitched, again.Phase)
	assert.Len(t, runtime.commands, 1, "resume must not repeat activation after durable switch")
}

func TestPostHandoffRecoveryDoesNotLeakRuntimeTokenInDialError(t *testing.T) {
	dataDir := t.TempDir()
	checkpoint := recoveryCheckpoint()
	secret := "post-handoff-recovery-secret"
	checkpoint.EnvFileReferences = []string{writeRecoveryRuntimeEnv(t, dataDir, checkpoint.MigrationID, checkpoint.ComponentGeneration, "GORDON_COMPONENT_RUNTIME_TOKEN="+secret+"\n")}
	store, err := NewMigrationCheckpointStore(migrationCheckpointPath(dataDir))
	require.NoError(t, err)
	require.NoError(t, store.Save(checkpoint))
	cfg := Config{}
	cfg.Server.DataDir = dataDir
	_, err = newPostHandoffMigrationRecovery(cfg, store, func(context.Context, RuntimeControlConfig) (RuntimeHandoffClient, error) {
		return nil, fmt.Errorf("dial refused token=%s", secret)
	})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
	assert.False(t, strings.Contains(err.Error(), "token="))
}

func recoveryCheckpoint() MigrationCheckpoint {
	return MigrationCheckpoint{
		MigrationID: "fixture", ComponentGeneration: 1, TargetImage: "example.invalid/gordon:v2", StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared,
		RuntimeChannelTransferred: true, BootstrapRuntimeEndpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock",
	}
}

func writeRecoveryRuntimeEnv(t *testing.T, dataDir, migrationID string, generation uint64, contents string) string {
	t.Helper()
	path := filepath.Join(dataDir, "migration", "env", migrationID, fmt.Sprintf("%d", generation), "runtime.env")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	return path
}
