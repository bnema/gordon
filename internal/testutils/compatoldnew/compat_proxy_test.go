package compatoldnew

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestManagedHTTPRouteScenarioDefinition(t *testing.T) {
	var managed Scenario
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == managedHTTPRouteScenarioName {
			managed = scenario
			break
		}
	}
	require.Equal(t, ScenarioStatusImplemented, managed.Status)
	require.False(t, managed.PodmanRequired)
	require.Empty(t, managed.BlockReason)
}

func TestManagedHTTPRoutePublishedAddressRejectsNonLoopback(t *testing.T) {
	address, err := managedProxyPublishedAddress("127.0.0.1:49152\n")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:49152", address)

	_, err = managedProxyPublishedAddress("0.0.0.0:49152\n")
	require.Error(t, err)
}

func TestCompatibilityManagedHTTPRoutePreflight(t *testing.T) {
	err := DockerCompatibilityPreflight(context.Background())
	if os.Getenv("GORDON_COMPAT_REQUIRE_RUNTIME") == "1" {
		require.NoError(t, err)
		return
	}
	if err != nil {
		t.Skipf("managed proxy compatibility runtime unavailable: %v", err)
	}
}

func TestCompatibilityManagedHTTPRoute(t *testing.T) {
	requireRealCompatibilityRun(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		if os.Getenv("GORDON_COMPAT_REQUIRE_RUNTIME") == "1" {
			t.Fatalf("managed proxy compatibility runtime required: %v", err)
		}
		t.Skipf("managed proxy compatibility runtime unavailable: %v", err)
	}

	artifactDir := compatibilityArtifactDir(t, "proxy")
	report, err := RunCompatibilityManagedHTTPRoute(ctx, projectRoot(t), artifactDir)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	require.NotEmpty(t, report.BaselineCommit)
	require.NotEmpty(t, report.CandidateCommit)
	require.Contains(t, report.RerunCommand, "GORDON_COMPAT_RUN_REAL=1")
	require.Contains(t, report.RerunCommand, "GORDON_COMPAT_REQUIRE_RUNTIME=1")
	require.Contains(t, report.RerunCommand, "TestCompatibilityManagedHTTPRoute")
	require.FileExists(t, filepath.Join(artifactDir, "compat-report.json"))

	for _, name := range []string{"old.raw.json", "new.raw.json", "compat-report.json"} {
		body, readErr := os.ReadFile(filepath.Join(artifactDir, name))
		require.NoError(t, readErr)
		require.NotContains(t, string(body), "docker inspect")
		require.NotContains(t, string(body), "\"Config\"")
	}
}
