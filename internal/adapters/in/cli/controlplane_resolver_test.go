package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveControlPlane_LocalDeniedWhenAuthEnabled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")

	remoteFlag = ""
	tokenFlag = ""
	insecureTLSFlag = false

	configPath := filepath.Join(tmpDir, "gordon.toml")
	err := os.WriteFile(configPath, []byte(`[server]
gordon_domain = "gordon.local"
data_dir = "`+filepath.Join(tmpDir, "data")+`"

[auth]
enabled = true
`), 0o600)
	if err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	handle, err := resolveControlPlane(configPath)
	if err == nil {
		if handle != nil {
			handle.close()
		}
		t.Fatalf("expected error when auth.enabled=true and no remote target")
	}
	if !strings.Contains(err.Error(), "local control plane is disabled when auth.enabled=true") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveControlPlane_LocalAllowedWhenAuthDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg"))
	t.Setenv("GORDON_REMOTE", "")
	t.Setenv("GORDON_TOKEN", "")

	remoteFlag = ""
	tokenFlag = ""
	insecureTLSFlag = false

	configPath := filepath.Join(tmpDir, "gordon.toml")
	err := os.WriteFile(configPath, []byte(`[server]
gordon_domain = "gordon.local"
data_dir = "`+filepath.Join(tmpDir, "data")+`"

[auth]
enabled = false
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
