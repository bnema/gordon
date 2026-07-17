package compatoldnew

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStageAdminAPISideHoldsDistinctPortsUntilReleased(t *testing.T) {
	setup, err := stageAdminAPISide(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, setup.releaseReservations())
		require.NoError(t, os.RemoveAll(setup.fixture.Root))
	})

	require.NotEqual(t, setup.port, setup.proxyPort)
	for _, port := range []int{setup.port, setup.proxyPort} {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		require.Error(t, err)
		if listener != nil {
			require.NoError(t, listener.Close())
		}
	}

	require.NoError(t, setup.releaseReservations())
	for _, port := range []int{setup.port, setup.proxyPort} {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		require.NoError(t, err)
		require.NoError(t, listener.Close())
	}
}

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
	require.Contains(t, report.RerunCommand, "GORDON_COMPAT_REQUIRE_RUNTIME=1")
	require.Contains(t, report.RerunCommand, "TestCompatibilityAdminAuthAndRouteCRUD")
	require.FileExists(t, filepath.Join(artifactDir, "compat-report.json"))
	require.FileExists(t, filepath.Join(artifactDir, "normalized.diff"))
}
