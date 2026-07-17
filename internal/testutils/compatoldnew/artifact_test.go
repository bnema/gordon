package compatoldnew

import (
	"os"
	"path/filepath"
	"testing"
)

const envCompatArtifactDir = "GORDON_COMPAT_ARTIFACT_DIR"

func compatibilityArtifactDir(t *testing.T, slice string) string {
	t.Helper()
	if root := os.Getenv(envCompatArtifactDir); root != "" {
		return filepath.Join(root, slice)
	}
	return filepath.Join(t.TempDir(), "artifacts")
}
