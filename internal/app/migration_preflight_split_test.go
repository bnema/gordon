package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func findPreflightCheck(t *testing.T, report MigrationPreflightReport, name string) PreflightCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("preflight check %q not found in report", name)
	return PreflightCheck{}
}

func splitTopologyConfig() Config {
	cfg := Config{}
	cfg.Control.Endpoint = "https://control.example.test:9443"
	return cfg
}

func TestMigrationPreflightSplitTopologyGuardPassesForMonolith(t *testing.T) {
	probes := passingMigrationProbes(nil)
	probes.SplitTopology = splitTopologyProbe(Config{})
	report := NewMigrationPreflight(probes).Check(context.Background())
	assert.True(t, report.Ready)
	assert.Equal(t, PreflightPass, findPreflightCheck(t, report, "split_topology").Status)
}

func TestMigrationPreflightSplitTopologyGuardFailsForSplitDeployment(t *testing.T) {
	probes := passingMigrationProbes(nil)
	probes.SplitTopology = splitTopologyProbe(splitTopologyConfig())
	report := NewMigrationPreflight(probes).Check(context.Background())
	assert.False(t, report.Ready)
	check := findPreflightCheck(t, report, "split_topology")
	assert.Equal(t, PreflightFail, check.Status)
	assert.Equal(t, PreflightConfig, check.Category)
	assert.Contains(t, check.Remediation, "already running in split mode")
	assert.Contains(t, check.Remediation, "migrate only converts a monolith deployment")
}

func TestMigrationServicePrepareRefusesSplitTopology(t *testing.T) {
	store, err := NewMigrationCheckpointStore(filepath.Join(t.TempDir(), "checkpoint.json"))
	require.NoError(t, err)
	probes := passingMigrationProbes(nil)
	probes.SplitTopology = splitTopologyProbe(splitTopologyConfig())
	launcher := &recordingComponentLauncher{}
	orchestrator, err := NewMigrationOrchestrator(NewMigrationPreflight(probes), store, launcher)
	require.NoError(t, err)
	service, err := NewMigrationService(NewMigrationPreflight(probes), store, MigrationEnvOptions{Config: splitTopologyConfig()})
	require.NoError(t, err)
	service.WithMigrationOrchestrator(orchestrator).WithMigrationCandidateImage("example.invalid/gordon:v3")

	_, err = service.Prepare(context.Background(), MigrationCheckpoint{MigrationID: "fixture-migration"})
	require.Error(t, err)
	assert.Empty(t, launcher.calls, "an already-split deployment must never launch migration components")
	require.NoFileExists(t, store.Path())
}
