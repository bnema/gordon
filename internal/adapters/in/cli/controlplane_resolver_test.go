package cli

import (
	"os"
	"path/filepath"
	"testing"
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
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	handle, err := resolveControlPlane(configPath)
	if err != nil {
		t.Fatalf("expected local control-plane handle, got error: %v", err)
	}
	if handle == nil || handle.plane == nil {
		t.Fatalf("expected non-nil local control-plane handle")
	}
	handle.close()
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
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	handle, err := resolveControlPlane(configPath)
	if err != nil {
		t.Fatalf("expected local control-plane handle, got error: %v", err)
	}
	if handle == nil || handle.plane == nil {
		t.Fatalf("expected non-nil local control-plane handle")
	}
	handle.close()
}
