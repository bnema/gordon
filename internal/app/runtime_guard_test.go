package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func fakeEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestProtectedRuntimePaths_AllSources(t *testing.T) {
	home := t.TempDir()
	env := fakeEnv(map[string]string{
		"XDG_RUNTIME_DIR":    "/run/user/1000",
		"DOCKER_HOST":        "unix:///x/d.sock",
		"GNUPGHOME":          "/keys/gnupg",
		"PASSWORD_STORE_DIR": "/keys/pass",
		"SOPS_AGE_KEY_FILE":  "/keys/age.txt",
		"XDG_CONFIG_HOME":    "/cfg",
	})
	got := protectedRuntimePaths("/data/gordon", "/etc/gordon/gordon.toml", "/custom/podman.sock", env, home)

	for _, want := range []string{
		"/data/gordon",
		"/etc/gordon/gordon.toml",
		"/var/run/docker.sock",
		"/run/docker.sock",
		"/run/podman/podman.sock",
		"/run/user/1000/docker.sock",
		"/run/user/1000/podman/podman.sock",
		"/run/user/1000/gordon",
		filepath.Join(home, ".gordon", "run"),
		"/x/d.sock",
		"/custom/podman.sock",
		"/keys/gnupg",
		filepath.Join(home, ".gnupg"),
		"/keys/pass",
		filepath.Join(home, ".password-store"),
		"/keys/age.txt",
		"/cfg/sops/age",
	} {
		assert.Contains(t, got, want)
	}
	assert.IsIncreasing(t, got)
}

func TestProtectedRuntimePaths_SkipsEmptyAndTCP(t *testing.T) {
	got := protectedRuntimePaths("/data", "", "", fakeEnv(map[string]string{"DOCKER_HOST": "tcp://h:2375"}), "")
	assert.Equal(t, []string{"/data", "/run/docker.sock", "/run/podman/podman.sock", "/var/run/docker.sock"}, got)
}

func TestProtectedRuntimePaths_SymlinkAddsBothForms(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	require.NoError(t, os.Mkdir(real, 0o700))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(real, link))
	realResolved, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)

	got := protectedRuntimePaths(link, "", "", fakeEnv(nil), "")
	assert.Contains(t, got, link)
	assert.Contains(t, got, realResolved)
}

func TestBuildAppRuntimeSafety(t *testing.T) {
	dataDir := t.TempDir()
	store, guard, err := buildAppRuntimeSafety(&serviceInit{}, dataDir)
	require.NoError(t, err)
	require.NotNil(t, store)
	assert.Equal(t, filepath.Join(dataDir, "secrets-run"), guard.SecretFilesDir)
	assert.Contains(t, guard.ProtectedPaths, dataDir)

	// The data dir itself cannot be bound into an app.
	err = guard.Check(&domain.ContainerConfig{Binds: []domain.ContainerBind{{Name: "all", Source: dataDir, Destination: "/data"}}})
	require.ErrorIs(t, err, domain.ErrRuntimeGuard)
}
