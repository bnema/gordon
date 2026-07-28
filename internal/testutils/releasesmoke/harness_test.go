package releasesmoke_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/testutils/releasesmoke"
)

func TestReleaseArtifactSmokeHarnessTable(t *testing.T) {
	harnessSource, err := releasesmoke.LoadHarnessSource(projectRoot(t))
	require.NoError(t, err)

	for _, contract := range releasesmoke.HarnessContracts {
		t.Run(contract.Name, func(t *testing.T) {
			for _, fragment := range contract.Contains {
				require.Contains(t, harnessSource, fragment, "harness must retain contract %q for %s", fragment, contract.Engine)
			}
			for _, fragment := range contract.NotContains {
				require.NotContains(t, harnessSource, fragment, "harness must not contain %q for %s", fragment, contract.Engine)
			}
		})
	}

	t.Run("constants", func(t *testing.T) {
		require.Equal(t, 30, releasesmoke.ReadinessPollAttempts)
		require.Equal(t, "Managed pass backend lock acquired", releasesmoke.ManagedPassLockMessage)
		require.Equal(t, "managed pass store is already in use", releasesmoke.LeaseConflictMessage)
		require.Equal(t, []string{"amd64", "arm64"}, releasesmoke.ImageArchitectures)
		require.Len(t, releasesmoke.RoleIdentities, 4)
	})

	t.Run("makefile-delegates-to-harness", func(t *testing.T) {
		makefile, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
		require.NoError(t, err)
		for _, tc := range releasesmoke.MakeDelegationTargets {
			recipe, err := releasesmoke.MakeTargetRecipe(string(makefile), tc.Target, tc.Next)
			require.NoError(t, err, tc.Target)
			require.NoError(t, releasesmoke.ValidateMakeDelegationRecipe(recipe), tc.Target)
		}
	})
}

func TestReleaseArtifactSmokeIntegration(t *testing.T) {
	if os.Getenv("GORDON_RELEASE_SMOKE_INTEGRATION") != "1" {
		t.Skip("set GORDON_RELEASE_SMOKE_INTEGRATION=1 to run release artifact smoke integration")
	}
	dist := os.Getenv("GORDON_RELEASE_SMOKE_DIST")
	if dist == "" {
		dist = filepath.Join(projectRoot(t), "dist")
	}
	h := releasesmoke.NewHarness(dist)
	if err := h.RunImageSmoke(t.Context()); err != nil {
		t.Fatalf("image smoke: %v", err)
	}
	if _, err := exec.LookPath("podman"); err == nil {
		if err := h.RunPodmanManagedPassSmoke(t.Context()); err != nil {
			t.Fatalf("podman managed pass smoke: %v", err)
		}
	}
}

func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
