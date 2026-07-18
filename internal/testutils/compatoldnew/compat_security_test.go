package compatoldnew

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompatibilitySecurityEdgeNoPodmanSocket(t *testing.T) {
	requireRealCompatibilityRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		t.Fatalf("security edge isolation requires Docker: %v", err)
	}
	report, err := RunSecurityEdgeNoPodmanSocket(ctx, projectRoot(t), compatibilityArtifactDir(t, "security-edge"))
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	assertSecurityArtifactsSafe(t, compatibilityArtifactDir(t, "security-edge"))
}

func TestCompatibilitySecurityRegistryNoPodmanSocket(t *testing.T) {
	requireRealCompatibilityRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := DockerCompatibilityPreflight(ctx); err != nil {
		t.Fatal(err)
	}
	artifactDir := compatibilityArtifactDir(t, "security-registry")
	report, err := RunSecurityRegistryNoPodmanSocket(ctx, projectRoot(t), artifactDir)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	assertSecurityArtifactsSafe(t, artifactDir)
}

func TestCompatibilitySecurityMissingComponentTokenRejected(t *testing.T) {
	requireRealCompatibilityRun(t)
	artifactDir := compatibilityArtifactDir(t, "security-auth-missing")
	report, err := RunSecurityComponentAuth(context.Background(), artifactDir, securityMissingToken)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	assertSecurityArtifactsSafe(t, artifactDir)
}

func TestCompatibilitySecurityWrongComponentTokenRejected(t *testing.T) {
	requireRealCompatibilityRun(t)
	artifactDir := compatibilityArtifactDir(t, "security-auth-wrong-component")
	report, err := RunSecurityComponentAuth(context.Background(), artifactDir, securityWrongComponentToken)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	assertSecurityArtifactsSafe(t, artifactDir)
}

func TestCompatibilitySecurityWrongScopeComponentTokenRejected(t *testing.T) {
	requireRealCompatibilityRun(t)
	artifactDir := compatibilityArtifactDir(t, "security-auth-scope")
	report, err := RunSecurityComponentAuth(context.Background(), artifactDir, securityWrongScopeToken)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	assertSecurityArtifactsSafe(t, artifactDir)
}

func assertSecurityArtifactsSafe(t *testing.T, artifactDir string) {
	t.Helper()
	for _, name := range []string{"old.raw.json", "new.raw.json", "old.normalized.json", "new.normalized.json", "compat-report.json", "normalized.diff"} {
		body, err := os.ReadFile(filepath.Join(artifactDir, name))
		require.NoError(t, err)
		for _, forbidden := range []string{"gordon_component", "authorization", "127.0.0.1:", "docker.sock", "podman.sock", "container-id"} {
			require.NotContains(t, strings.ToLower(string(body)), forbidden, name)
		}
	}
}
