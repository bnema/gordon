package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKernelRejectsSplitModeControlEndpoint(t *testing.T) {
	configPath := writeSplitModeKernelConfig(t)

	kernel, err := NewKernel(configPath)
	require.Error(t, err)
	require.Nil(t, kernel)
	require.Contains(t, err.Error(), "local kernel initialization disabled")
	require.Contains(t, err.Error(), "control")
	require.Contains(t, err.Error(), "https://control.example.com")
}

func TestKernelQuietRejectsSplitModeControlEndpoint(t *testing.T) {
	configPath := writeSplitModeKernelConfig(t)

	kernel, err := NewKernelQuiet(configPath)
	require.Error(t, err)
	require.Nil(t, kernel)
	require.Contains(t, err.Error(), "local kernel initialization disabled")
	require.Contains(t, err.Error(), "https://control.example.com")
}

func writeSplitModeKernelConfig(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "gordon.toml")
	contents := `[server]
gordon_domain = "gordon.local"
data_dir = "` + filepath.Join(tmpDir, "data") + `"

[control]
endpoint = "https://control.example.com"

[auth]
enabled = false
secrets_backend = "unsafe"
`
	require.NoError(t, os.WriteFile(configPath, []byte(contents), 0o600))
	return configPath
}
