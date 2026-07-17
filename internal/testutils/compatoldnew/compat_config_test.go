package compatoldnew

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompatibilityConfigShowJSON(t *testing.T) {
	requireRealCompatibilityRun(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	artifactDir := compatibilityArtifactDir(t, "config")
	report, err := RunConfigShowJSON(ctx, projectRoot(t), artifactDir)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	require.NotEmpty(t, report.BaselineCommit)
	require.NotEmpty(t, report.CandidateCommit)
	require.Contains(t, report.RerunCommand, "GORDON_COMPAT_RUN_REAL=1")
	require.Contains(t, report.RerunCommand, "TestCompatibilityConfigShowJSON")
	require.FileExists(t, filepath.Join(artifactDir, "compat-report.json"))
	require.FileExists(t, filepath.Join(artifactDir, "normalized.diff"))
}

func projectRoot(t *testing.T) string {
	t.Helper()
	return filepath.Clean(filepath.Join(FixtureRoot(), "..", "..", "..", ".."))
}
