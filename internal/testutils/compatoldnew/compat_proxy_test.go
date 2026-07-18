package compatoldnew

import (
	"context"
	"encoding/json"
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

func TestDistributedDrainScenarioDefinition(t *testing.T) {
	var distributed Scenario
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == distributedDrainScenarioName {
			distributed = scenario
			break
		}
	}
	require.Equal(t, ScenarioStatusImplemented, distributed.Status)
	require.False(t, distributed.PodmanRequired)
	require.Empty(t, distributed.BlockReason)
}

func TestTrafficProtocolScenarioDefinition(t *testing.T) {
	var matrix Scenario
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == trafficProtocolScenarioName {
			matrix = scenario
			break
		}
	}
	require.Equal(t, ScenarioStatusImplemented, matrix.Status)
	require.False(t, matrix.PodmanRequired)
	require.Empty(t, matrix.BlockReason)
}

func TestTrafficGraphStreamScenarioDefinition(t *testing.T) {
	var matrix Scenario
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == trafficGraphStreamScenarioName {
			matrix = scenario
			break
		}
	}
	require.Equal(t, ScenarioStatusImplemented, matrix.Status)
	require.False(t, matrix.PodmanRequired)
	require.Empty(t, matrix.BlockReason)
}

func TestSplitDeploymentDrainScenarioRemainsPending(t *testing.T) {
	var split Scenario
	for _, scenario := range ProxyScenarios() {
		if scenario.Name == "proxy/split-deployment-drain" {
			split = scenario
			break
		}
	}
	require.Equal(t, ScenarioStatusPending, split.Status)
	require.Contains(t, split.BlockReason, "WS07")
	require.Contains(t, split.BlockReason, "bootstrap")
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

func TestCompatibilityTrafficProtocolMatrix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	matrix, err := RunTrafficProtocolMatrix(ctx)
	require.NoError(t, err)
	require.Len(t, matrix.Checks, 7)
	for _, check := range matrix.Checks {
		require.True(t, check.Passed, check.Protocol)
		require.Equal(t, "ok", check.Status, check.Protocol)
	}
	artifact, err := json.Marshal(matrix)
	require.NoError(t, err)
	for _, forbidden := range []string{"127.0.0.1:", "CERTIFICATE", "PRIVATE KEY", "container", "token"} {
		require.NotContains(t, string(artifact), forbidden)
	}
}

func TestCompatibilityTrafficProtocolFailClosed(t *testing.T) {
	require.NoError(t, ValidateTrafficProtocolFailClosed())
}

func TestCompatibilityTrafficGraphStreamMatrix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	matrix, err := RunTrafficGraphStreamMatrix(ctx)
	require.NoError(t, err)
	require.Len(t, matrix.Checks, 7)
	for _, check := range matrix.Checks {
		require.True(t, check.Passed, check.Protocol)
		require.Equal(t, "ok", check.Status, check.Protocol)
	}
}

func TestSplitEdgeTLSCompatibilityExceptionIsExplicit(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(projectRoot(t), "docs", "config", "edge.md"))
	require.NoError(t, err)
	documentation := string(body)
	require.Contains(t, documentation, "Gordon-managed ACME issuance and challenge handling remain monolith-only")
	require.Contains(t, documentation, "mode = \"files\"")
	require.Contains(t, documentation, "mode = \"external\"")
	require.Contains(t, documentation, "never silently falls back")
}

func TestCompatibilityDistributedDrainProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	artifactDir := compatibilityArtifactDir(t, "proxy-distributed-drain")
	report, err := RunCompatibilityDistributedDrainProtocol(ctx, artifactDir)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	require.FileExists(t, filepath.Join(artifactDir, "compat-report.json"))
	for _, name := range []string{"old.raw.json", "new.raw.json", "compat-report.json"} {
		body, readErr := os.ReadFile(filepath.Join(artifactDir, name))
		require.NoError(t, readErr)
		require.NotContains(t, string(body), "private-old-backing")
		require.NotContains(t, string(body), "gordon_component.")
	}
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

	beforeTags, beforeContainers, beforeVolumes := zeroDowntimeDrainDockerResources(t, ctx)
	require.Empty(t, beforeTags, "zero downtime drain image tags must not leak between runs")
	require.Empty(t, beforeContainers, "zero downtime drain containers must not leak between runs")
	require.Empty(t, beforeVolumes, "zero downtime drain state volumes must not leak between runs")
	t.Cleanup(func() {
		afterTags, afterContainers, afterVolumes := zeroDowntimeDrainDockerResources(t, context.Background())
		require.Equal(t, beforeTags, afterTags, "zero downtime drain image tag set changed")
		require.Equal(t, beforeContainers, afterContainers, "zero downtime drain container set changed")
		require.Equal(t, beforeVolumes, afterVolumes, "zero downtime drain state volume set changed")
		require.Empty(t, afterTags, "zero downtime drain image tags leaked")
		require.Empty(t, afterContainers, "zero downtime drain containers leaked")
		require.Empty(t, afterVolumes, "zero downtime drain state volumes leaked")
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

func zeroDowntimeDrainDockerResources(t *testing.T, ctx context.Context) ([]string, []string, []string) {
	t.Helper()
	imageOutput, err := dockerCompatibilityOutput(ctx, "image", "ls", "--format", "{{.Repository}}:{{.Tag}}")
	require.NoError(t, err, "list zero downtime drain image tags")
	containerOutput, err := dockerCompatibilityOutput(ctx, "ps", "-a", "--format", "{{.Names}}")
	require.NoError(t, err, "list zero downtime drain containers")
	volumeOutput, err := dockerCompatibilityOutput(ctx, "volume", "ls", "--format", "{{.Name}}")
	require.NoError(t, err, "list zero downtime drain state volumes")

	var tags, containers, volumes []string
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
	for _, name := range strings.Fields(volumeOutput) {
		if strings.HasPrefix(name, "gordon-compat-zero-drain-state-") {
			volumes = append(volumes, name)
		}
	}
	sort.Strings(tags)
	sort.Strings(containers)
	sort.Strings(volumes)
	return tags, containers, volumes
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
