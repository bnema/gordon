package compatoldnew

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCompatibilityAdminAPIPreflight(t *testing.T) {
	err := AdminAPIPreflight(context.Background())
	if os.Getenv("GORDON_COMPAT_REQUIRE_RUNTIME") == "1" {
		require.NoError(t, err)
		return
	}
	if err != nil {
		t.Skipf("admin API compatibility runtime unavailable: %v", err)
	}
}

func TestCompatibilityAdminAuthAndRouteCRUD(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if err := AdminAPIPreflight(ctx); err != nil {
		if os.Getenv("GORDON_COMPAT_REQUIRE_RUNTIME") == "1" {
			t.Fatalf("admin API compatibility runtime required: %v", err)
		}
		t.Skipf("admin API compatibility runtime unavailable: %v", err)
	}

	artifactDir := compatibilityArtifactDir(t, "api")
	report, err := RunCompatibilityAdminAuthAndRouteCRUD(ctx, projectRoot(t), artifactDir)
	require.NoError(t, err)
	require.Zero(t, report.Failed, report.ConsoleSummary())
	require.NotEmpty(t, report.BaselineCommit)
	require.NotEmpty(t, report.CandidateCommit)
	require.Contains(t, report.RerunCommand, "TestCompatibilityAdminAuthAndRouteCRUD")
	require.FileExists(t, filepath.Join(artifactDir, "compat-report.json"))
	require.FileExists(t, filepath.Join(artifactDir, "normalized.diff"))
}
