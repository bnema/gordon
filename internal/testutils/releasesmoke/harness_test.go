package releasesmoke_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/testutils/releasesmoke"
)

func TestReleaseArtifactSmokeHarnessTable(t *testing.T) {
	harnessSource := readReleaseSmokeSources(t)

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
		makefile := readMakefile(t)
		for _, tc := range []struct {
			target string
			next   string
		}{
			{target: "release-podman-managed-pass-smoke", next: "release-image-smoke"},
			{target: "release-image-smoke", next: "build-push"},
		} {
			recipe := makeTargetRecipe(t, makefile, tc.target, tc.next)
			require.Contains(t, recipe, "go run ./cmd/release-smoke", tc.target)
			require.NotContains(t, recipe, "readiness_pid=$$!")
			require.NotContains(t, recipe, "podman system service")
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

func readReleaseSmokeSources(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	var b strings.Builder
	for _, name := range []string{
		"internal/testutils/releasesmoke/harness.go",
		"internal/testutils/releasesmoke/podman.go",
	} {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String()
}

func readMakefile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(projectRoot(t), "Makefile"))
	require.NoError(t, err)
	return string(data)
}

func makeTargetRecipe(t *testing.T, makefile, target, nextTarget string) string {
	t.Helper()
	startPattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `:`)
	startLoc := startPattern.FindStringIndex(makefile)
	require.NotNil(t, startLoc, "missing Make target %s", target)
	start := startLoc[0]
	endPattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(nextTarget) + `:`)
	endLoc := endPattern.FindStringIndex(makefile[start:])
	require.NotNil(t, endLoc, "missing target boundary %s", nextTarget)
	return makefile[start : start+endLoc[0]]
}

func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}
