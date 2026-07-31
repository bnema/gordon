package releasesmoke

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadHarnessSourceRequiresAllContractFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, rel := range HarnessSourceRelPaths[:len(HarnessSourceRelPaths)-1] {
		path := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("package releasesmoke\n"), 0o644))
	}

	_, err := LoadHarnessSource(root)
	require.Error(t, err)
	require.Contains(t, err.Error(), HarnessSourceRelPaths[len(HarnessSourceRelPaths)-1])
}

func TestLoadHarnessSourceRejectsEmptyRoot(t *testing.T) {
	t.Parallel()
	_, err := LoadHarnessSource("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "root")
}

func TestMakeTargetRecipeFailClosed(t *testing.T) {
	t.Parallel()

	makefile := "release-image-smoke:\n\t@echo ok\nbuild-push:\n\t@true\n"

	t.Run("missing target", func(t *testing.T) {
		t.Parallel()
		_, err := MakeTargetRecipe(makefile, "missing-target", "build-push")
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing Make target")
	})

	t.Run("missing boundary", func(t *testing.T) {
		t.Parallel()
		_, err := MakeTargetRecipe(makefile, "release-image-smoke", "missing-next")
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing target boundary")
	})

	t.Run("empty target", func(t *testing.T) {
		t.Parallel()
		_, err := MakeTargetRecipe(makefile, "", "build-push")
		require.Error(t, err)
		require.Contains(t, err.Error(), "target")
	})
}

func TestValidateMakeDelegationRecipeFailClosed(t *testing.T) {
	t.Parallel()

	t.Run("missing harness delegation", func(t *testing.T) {
		t.Parallel()
		err := ValidateMakeDelegationRecipe("release-image-smoke:\n\tdocker run ...\n")
		require.Error(t, err)
		require.Contains(t, err.Error(), "go run ./cmd/release-smoke")
	})

	t.Run("inline readiness pid", func(t *testing.T) {
		t.Parallel()
		err := ValidateMakeDelegationRecipe("go run ./cmd/release-smoke\nreadiness_pid=$$!\n")
		require.Error(t, err)
		require.Contains(t, err.Error(), "readiness_pid=$$!")
	})

	t.Run("inline podman system service", func(t *testing.T) {
		t.Parallel()
		err := ValidateMakeDelegationRecipe("go run ./cmd/release-smoke\npodman system service\n")
		require.Error(t, err)
		require.Contains(t, err.Error(), "podman system service")
	})
}

func TestCharacterizationHelpersMatchRepositoryContracts(t *testing.T) {
	root := repoRoot(t)

	source, err := LoadHarnessSource(root)
	require.NoError(t, err)
	require.Contains(t, source, "ManagedPassArtifactShellCheck")
	require.NotEmpty(t, ManagedPassArtifactShellCheck)

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	require.NoError(t, err)
	require.NotEmpty(t, MakeDelegationTargets)
	for _, tc := range MakeDelegationTargets {
		recipe, err := MakeTargetRecipe(string(makefile), tc.Target, tc.Next)
		require.NoError(t, err, tc.Target)
		require.NoError(t, ValidateMakeDelegationRecipe(recipe), tc.Target)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}
