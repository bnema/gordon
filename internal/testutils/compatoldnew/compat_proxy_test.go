package compatoldnew

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func TestZeroDowntimeDrainScenarioDefinition(t *testing.T) {
	var drain Scenario
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == zeroDowntimeDrainScenarioName {
			drain = scenario
			break
		}
	}
	require.Equal(t, ScenarioStatusImplemented, drain.Status)
	require.False(t, drain.PodmanRequired)
	require.Empty(t, drain.BlockReason)
}

func TestExternalRouteScenarioDefinition(t *testing.T) {
	var external Scenario
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == externalRouteScenarioName {
			external = scenario
			break
		}
	}
	require.Equal(t, ScenarioStatusImplemented, external.Status)
	require.False(t, external.PodmanRequired)
	require.Empty(t, external.BlockReason)
}

func TestExternalRouteSubnetIsSafeCGNAT(t *testing.T) {
	_, subnet, err := net.ParseCIDR(externalRouteSubnet(1234))
	require.NoError(t, err)
	require.True(t, cgnatCIDR.Contains(subnet.IP))
	require.Equal(t, 24, ones(subnet))
}

func ones(subnet *net.IPNet) int {
	ones, _ := subnet.Mask.Size()
	return ones
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

func TestCompatibilityExternalRoute(t *testing.T) {
	requireRealCompatibilityRun(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// A selected real target must fail rather than turn a missing runtime into
	// a green skip. CI invokes it with both explicit gates from the rerun command.
	require.NoError(t, DockerCompatibilityPreflight(ctx), "external route compatibility runtime required")

	artifactDir := compatibilityArtifactDir(t, "proxy-external")
	report, err := RunCompatibilityExternalRoute(ctx, projectRoot(t), artifactDir)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	require.NotEmpty(t, report.BaselineCommit)
	require.NotEmpty(t, report.CandidateCommit)
	require.Contains(t, report.RerunCommand, "GORDON_COMPAT_RUN_REAL=1")
	require.Contains(t, report.RerunCommand, "GORDON_COMPAT_REQUIRE_RUNTIME=1")
	require.Contains(t, report.RerunCommand, "TestCompatibilityExternalRoute")
	require.FileExists(t, filepath.Join(artifactDir, "compat-report.json"))

	for _, name := range []string{"old.raw.json", "new.raw.json", "compat-report.json"} {
		body, readErr := os.ReadFile(filepath.Join(artifactDir, name))
		require.NoError(t, readErr)
		require.NotContains(t, string(body), "docker inspect")
		require.NotContains(t, string(body), "\"Config\"")
		require.NotContains(t, string(body), "containerID")
	}
}

func TestCompatibilityZeroDowntimeDrain(t *testing.T) {
	requireRealCompatibilityRun(t)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	require.NoError(t, DockerCompatibilityPreflight(ctx), "zero downtime drain compatibility runtime required")

	beforeTags, beforeContainers := zeroDowntimeDrainDockerResources(t, ctx)
	require.Empty(t, beforeTags, "zero downtime drain image tags must not leak between runs")
	require.Empty(t, beforeContainers, "zero downtime drain containers must not leak between runs")
	t.Cleanup(func() {
		afterTags, afterContainers := zeroDowntimeDrainDockerResources(t, context.Background())
		require.Equal(t, beforeTags, afterTags, "zero downtime drain image tag set changed")
		require.Equal(t, beforeContainers, afterContainers, "zero downtime drain container set changed")
		require.Empty(t, afterTags, "zero downtime drain image tags leaked")
		require.Empty(t, afterContainers, "zero downtime drain containers leaked")
	})

	artifactDir := compatibilityArtifactDir(t, "proxy-zero-drain")
	report, err := RunCompatibilityZeroDowntimeDrain(ctx, projectRoot(t), artifactDir)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	require.NotEmpty(t, report.BaselineCommit)
	require.NotEmpty(t, report.CandidateCommit)
	require.Contains(t, report.RerunCommand, "GORDON_COMPAT_RUN_REAL=1")
	require.Contains(t, report.RerunCommand, "GORDON_COMPAT_REQUIRE_RUNTIME=1")
	require.Contains(t, report.RerunCommand, "TestCompatibilityZeroDowntimeDrain")
	require.FileExists(t, filepath.Join(artifactDir, "compat-report.json"))
	for _, name := range []string{"old.raw.json", "new.raw.json", "compat-report.json"} {
		body, readErr := os.ReadFile(filepath.Join(artifactDir, name))
		require.NoError(t, readErr)
		for _, forbidden := range []string{"docker inspect", "\"Config\"", "containerID", "localhost:", "Bearer ", "eyJ"} {
			require.NotContains(t, string(body), forbidden)
		}
	}
}

func zeroDowntimeDrainDockerResources(t *testing.T, ctx context.Context) ([]string, []string) {
	t.Helper()
	imageOutput, err := dockerCompatibilityOutput(ctx, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
	require.NoError(t, err, "list zero downtime drain image tags")
	containerOutput, err := dockerCompatibilityOutput(ctx, "ps", "-a", "--format", "{{.Names}}")
	require.NoError(t, err, "list zero downtime drain containers")

	var tags, containers []string
	for _, ref := range strings.Fields(imageOutput) {
		if strings.HasPrefix(ref, "gordon-compat-zero-drain:") ||
			(strings.HasPrefix(ref, "localhost:") && strings.Contains(ref, "/gordon-compat-drain-")) {
			tags = append(tags, ref)
		}
	}
	for _, name := range strings.Fields(containerOutput) {
		if strings.HasPrefix(name, "gordon-drain-") {
			containers = append(containers, name)
		}
	}
	sort.Strings(tags)
	sort.Strings(containers)
	return tags, containers
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
