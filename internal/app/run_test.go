package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveRuntimeConfig(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		xdgDir     string // non-empty means set XDG_RUNTIME_DIR
		createSock bool   // whether to create the socket file under xdgDir
		want       string
	}{
		{
			name:  "empty string returns empty",
			value: "",
			want:  "",
		},
		{
			name:  "auto returns empty",
			value: "auto",
			want:  "",
		},
		{
			name:       "podman with XDG_RUNTIME_DIR and socket present",
			value:      "podman",
			xdgDir:     "tmpXDG", // replaced with t.TempDir() in the loop
			createSock: true,
		},
		{
			name:   "podman with XDG_RUNTIME_DIR but socket absent falls back to system socket",
			value:  "podman",
			xdgDir: "tmpXDG",
			want:   "/run/podman/podman.sock",
		},
		{
			name:  "podman without XDG_RUNTIME_DIR falls back to system socket",
			value: "podman",
			want:  "/run/podman/podman.sock",
		},
		{
			name:  "docker returns well-known path",
			value: "docker",
			want:  "/var/run/docker.sock",
		},
		{
			name:  "unix:// prefix is stripped",
			value: "unix:///custom/path.sock",
			want:  "/custom/path.sock",
		},
		{
			name:  "bare path returned as-is",
			value: "/custom/path.sock",
			want:  "/custom/path.sock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			xdgDir := tt.xdgDir
			if xdgDir == "tmpXDG" {
				xdgDir = t.TempDir()
			}

			if xdgDir != "" {
				t.Setenv("XDG_RUNTIME_DIR", xdgDir)
			} else {
				t.Setenv("XDG_RUNTIME_DIR", "")
			}

			want := tt.want
			if tt.createSock && xdgDir != "" {
				sockDir := filepath.Join(xdgDir, "podman")
				os.MkdirAll(sockDir, 0o755)
				os.WriteFile(filepath.Join(sockDir, "podman.sock"), nil, 0o600)
				want = filepath.Join(xdgDir, "podman", "podman.sock")
			}

			got := resolveRuntimeConfig(tt.value)
			assert.Equal(t, want, got)
		})
	}
}
