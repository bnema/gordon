package compatoldnew

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const envCompatArtifactDir = "GORDON_COMPAT_ARTIFACT_DIR"

func TestCompatibilityArtifactDirRelativeOverrideUsesRepositoryRoot(t *testing.T) {
	t.Setenv(envCompatArtifactDir, filepath.Join("artifacts", "compat"))

	root := projectRoot(t)
	for _, slice := range []string{"config", "cli", "api"} {
		t.Run(slice, func(t *testing.T) {
			require.Equal(t, filepath.Join(root, "artifacts", "compat", slice), compatibilityArtifactDir(t, slice))
		})
	}
}

func TestCompatibilityArtifactDirPreservesAbsoluteOverride(t *testing.T) {
	root := filepath.Join(t.TempDir(), "compat-reports")
	t.Setenv(envCompatArtifactDir, root)

	require.Equal(t, filepath.Join(root, "config"), compatibilityArtifactDir(t, "config"))
}

func compatibilityArtifactDir(t *testing.T, slice string) string {
	t.Helper()
	if root := os.Getenv(envCompatArtifactDir); root != "" {
		if filepath.IsAbs(root) {
			return filepath.Join(root, slice)
		}
		return filepath.Join(projectRoot(t), root, slice)
	}
	return filepath.Join(t.TempDir(), "artifacts")
}
