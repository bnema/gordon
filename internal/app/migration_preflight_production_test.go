package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

type productionPreflightState struct {
	snapshot domain.RuntimeActualStateSnapshot
}

func (s productionPreflightState) SubscribeRuntimeState(context.Context) (<-chan domain.RuntimeActualStateSnapshot, error) {
	updates := make(chan domain.RuntimeActualStateSnapshot, 1)
	updates <- s.snapshot
	close(updates)
	return updates, nil
}

type productionPreflightRuntime struct {
	report    out.RuntimeEnvironment
	listeners []bool
}

func (r productionPreflightRuntime) ProbeRuntimeEnvironment(context.Context) (out.RuntimeEnvironment, error) {
	return r.report, nil
}

func (r productionPreflightRuntime) ProbePublicListeners(context.Context, []int) ([]bool, error) {
	return r.listeners, nil
}

func TestControlMigrationPreflightUsesReadOnlyProductionProbes(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dataDir, "registry"), 0o750))
	require.NoError(t, os.Mkdir(filepath.Join(dataDir, "env"), 0o700))
	configPath := filepath.Join(dataDir, "gordon.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[auth]\nsecrets_backend = \"unsafe\"\n"), 0o600))
	cfg := Config{}
	cfg.Server.DataDir = dataDir
	cfg.Auth.SecretsBackend = "unsafe"
	runtime := productionPreflightRuntime{report: out.RuntimeEnvironment{
		Engine: "podman", Rootless: true, APIReachable: true, ImageAvailable: true,
		ImagePullable: true, NetworkFeasible: true, DiskSufficient: true,
	}, listeners: []bool{false, false}}
	state := productionPreflightState{snapshot: domain.RuntimeActualStateSnapshot{
		Generation: 1, StateVersion: "fixture-state", SourceComponentID: "fixture-runtime",
	}}

	before, err := os.ReadDir(dataDir)
	require.NoError(t, err)
	report := newControlMigrationPreflight(configPath, cfg, runtime, state).Check(context.Background())
	after, err := os.ReadDir(dataDir)
	require.NoError(t, err)

	assert.True(t, report.Ready)
	require.Len(t, report.Checks, 13)
	for _, check := range report.Checks {
		assert.Equalf(t, PreflightPass, check.Status, "%s: %s", check.Name, check.Remediation)
	}
	assert.Equal(t, before, after, "preflight must not create files, networks, or checkpoints")
	_, err = os.Stat(filepath.Join(dataDir, "migration"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestColdPublicListenerProbeRejectsManagedMonolithOccupiedPorts(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port = 18443
	cfg.Server.RegistryPort = 15000
	for _, listeners := range [][]bool{{false, false}, {true, false}, {false, true}} {
		runtime := productionPreflightRuntime{listeners: listeners}
		assert.Error(t, publicListenerProbe(runtime, cfg)(context.Background()), "occupied listeners must fail cold preflight")
	}
	runtime := productionPreflightRuntime{listeners: []bool{true, true}}
	assert.NoError(t, publicListenerProbe(runtime, cfg)(context.Background()))
}

func TestProductionPreflightProbesFailClosed(t *testing.T) {
	dataDir := t.TempDir()
	registry := filepath.Join(dataDir, "registry")
	envDir := filepath.Join(dataDir, "env")
	require.NoError(t, os.Mkdir(registry, 0o750))
	require.NoError(t, os.Mkdir(envDir, 0o700))

	t.Run("rejects symlinked registry storage", func(t *testing.T) {
		link := filepath.Join(dataDir, "registry-link")
		require.NoError(t, os.Symlink(registry, link))
		assert.Error(t, directoryAccessProbe(link, 4)(context.Background()))
	})
	t.Run("rejects symlinked environment files", func(t *testing.T) {
		link := filepath.Join(envDir, "required.env")
		require.NoError(t, os.Symlink(filepath.Join(dataDir, "missing"), link))
		assert.Error(t, environmentDirectoryProbe(envDir)(context.Background()))
		require.NoError(t, os.Remove(link))
	})
	t.Run("rejects runtime-confirmed managed monolith listener", func(t *testing.T) {
		cfg := Config{}
		cfg.Server.Port = 18443
		runtime := productionPreflightRuntime{listeners: []bool{false}}
		assert.Error(t, publicListenerProbe(runtime, cfg)(context.Background()))
	})
	t.Run("rejects unrelated owner and incomplete runtime response", func(t *testing.T) {
		cfg := Config{}
		cfg.Server.Port = 18443
		assert.Error(t, publicListenerProbe(productionPreflightRuntime{listeners: []bool{false}}, cfg)(context.Background()))
		assert.Error(t, publicListenerProbe(productionPreflightRuntime{}, cfg)(context.Background()))
	})
	t.Run("requires sanitized runtime inventory", func(t *testing.T) {
		state := productionPreflightState{}
		assert.Error(t, managedRuntimeInventoryProbe(state)(context.Background()))
	})
}
