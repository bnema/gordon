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
	recovery, err := newPostHandoffMigrationRecovery(cfg, store, func(_ context.Context, target RuntimeControlConfig) (RuntimeHandoffClient, error) {
		got = target
		return recoveryRuntime{}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, recovery)
	assert.Equal(t, "unix://"+filepath.Join(dataDir, "migration", "fixture", bootstrapRuntimeSocketName), got.Endpoint)
	assert.Equal(t, "generated-replacement-runtime-token", got.Token)
	assert.Empty(t, got.TokenEnv)
	assert.NotEqual(t, cfg.Runtime.Token, got.Token)

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
