package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveControlPlane_LocalAllowedWhenAuthEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	originalRemoteFlag, originalTokenFlag, originalInsecureTLSFlag := remoteFlag, tokenFlag, insecureTLSFlag
	t.Cleanup(func() {
		remoteFlag = originalRemoteFlag
		tokenFlag = originalTokenFlag
		insecureTLSFlag = originalInsecureTLSFlag
	})

	remoteFlag = ""
	tokenFlag = ""
	insecureTLSFlag = false

	configPath := filepath.Join(tmpDir, "gordon.toml")
	err := os.WriteFile(configPath, []byte(`[server]
gordon_domain = "gordon.local"
data_dir = "`+filepath.Join(tmpDir, "data")+`"

[auth]
enabled = true
secrets_backend = "unsafe"
`), 0o600)
	require.NoError(t, err)

	handle, err := resolveControlPlane(configPath)
	require.NoError(t, err)
	require.NotNil(t, handle)
	require.NotNil(t, handle.plane)
	defer handle.close()
}

func TestResolveControlPlane_LocalAllowedWhenAuthDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")
	originalRemoteFlag, originalTokenFlag, originalInsecureTLSFlag := remoteFlag, tokenFlag, insecureTLSFlag
	t.Cleanup(func() {
		remoteFlag = originalRemoteFlag
		tokenFlag = originalTokenFlag
		insecureTLSFlag = originalInsecureTLSFlag
	})

	remoteFlag = ""
	tokenFlag = ""
	insecureTLSFlag = false

	configPath := filepath.Join(tmpDir, "gordon.toml")
	err := os.WriteFile(configPath, []byte(`[server]
gordon_domain = "gordon.local"
data_dir = "`+filepath.Join(tmpDir, "data")+`"

[auth]
enabled = false
secrets_backend = "unsafe"
`), 0o600)
	require.NoError(t, err)

	handle, err := resolveControlPlane(configPath)
	require.NoError(t, err)
	require.NotNil(t, handle)
	require.NotNil(t, handle.plane)
	defer handle.close()
}
