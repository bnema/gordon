package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewKernel(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "gordon.toml")
	dataDir := filepath.Join(tmpDir, "data")

	cfg := fmt.Sprintf(`[server]
gordon_domain = "gordon.local"
data_dir = %q

[auth]
enabled = false
`, dataDir)

	err := os.WriteFile(cfgPath, []byte(cfg), 0o600)
	require.NoError(t, err)

	kernel, err := NewKernel(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, kernel)
	t.Cleanup(func() { require.NoError(t, kernel.Close()) })

	require.NotNil(t, kernel.Config())
	require.NotNil(t, kernel.Secrets())
}
