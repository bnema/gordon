package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/secretfiles"
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

		"GORDON_CLOUDFLARE_API_TOKEN_FILE": "/keys/cf-token",
	})
	got := protectedRuntimePaths("/data/gordon", []string{"/etc/gordon/gordon.toml", "/custom/podman.sock", ""}, env, home)

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
		"/keys/cf-token",
		filepath.Join(home, ".local", "share", "containers"),
		filepath.Join(home, ".local", "share", "docker"),
		"/cfg/containers",
		filepath.Join(home, ".docker"),
		"/cfg/systemd/user",
	} {
		assert.Contains(t, got, want)
	}
	assert.IsIncreasing(t, got)
}

func TestProtectedRuntimePaths_SkipsEmptyAndTCP(t *testing.T) {
	// Subset, not Equal: on hosts where a fixed path crosses a symlink
	// (for example /var -> /private/var) its resolved form is added too.
	got := protectedRuntimePaths("/data", nil, fakeEnv(map[string]string{"DOCKER_HOST": "tcp://h:2375"}), "")
	assert.Subset(t, got, []string{"/data", "/run/docker.sock", "/run/podman/podman.sock", "/var/run/docker.sock"})
	for _, p := range got {
		assert.NotContains(t, p, "tcp:")
		assert.NotContains(t, p, "h:2375")
	}
}

func TestProtectedRuntimePaths_SymlinkAddsBothForms(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	require.NoError(t, os.Mkdir(real, 0o700))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(real, link))
	realResolved, err := filepath.EvalSymlinks(real)
	require.NoError(t, err)

	got := protectedRuntimePaths(link, nil, fakeEnv(nil), "")
	assert.Contains(t, got, link)
	assert.Contains(t, got, realResolved)

	// A protected path that does not exist yet under a symlinked parent
	// is protected in its resolved form too.
	missing := filepath.Join(link, "later", "store")
	got = protectedRuntimePaths("/data", []string{missing}, fakeEnv(nil), "")
	assert.Contains(t, got, missing)
	assert.Contains(t, got, filepath.Join(realResolved, "later", "store"))
}

func TestConfiguredGordonPaths(t *testing.T) {
	si := &serviceInit{}
	si.cfg.Server.TLSKeyFile = "/tls/key.pem"
	si.cfg.Server.DataDir = "/data"
	si.cfg.Logging.AccessLog.FilePath = "/logs/access.log"
	si.cfg.Backups.Databases.StorageDir = "/backups"

	got := configuredGordonPaths(si)
	assert.Contains(t, got, "/tls/key.pem")
	assert.Contains(t, got, "/backups")
	assert.NotContains(t, got, "/logs/access.log")
}

func TestGordonLogLocations(t *testing.T) {
	var cfg Config
	cfg.Server.DataDir = "/data"
	cfg.Logging.AccessLog.FilePath = "/logs/access.log"
	assert.Empty(t, gordonLogLocations(cfg, "/data"), "disabled logs grant nothing")

	cfg.Logging.File.Enabled = true
	cfg.Logging.AccessLog.Enabled = true
	cfg.Logging.AccessLog.Output = "file"
	cfg.Logging.ContainerLogs.Enabled = true
	assert.ElementsMatch(t, []logLocation{
		{dir: "/data/logs", path: "/data/logs/gordon.log"},
		{dir: "/logs", path: "/logs/access.log"},
		{dir: "/data/logs/containers", path: "/data/logs/containers"},
	}, gordonLogLocations(cfg, "/data"))

	cfg.Logging.AccessLog.Output = "stdout"
	assert.Len(t, gordonLogLocations(cfg, "/data"), 2, "non-file access log grants nothing")
}

func TestReadableLogDirs(t *testing.T) {
	protected := []string{"/data", "/home/u", "/home/u/.gnupg", "/srv/logs/keys"}
	loc := func(dir string) logLocation { return logLocation{dir: dir, path: dir + "/x.log"} }
	readable, keep := readableLogDirs([]logLocation{
		loc("/data/logs"),
		loc("/data/logs/containers"),
		loc("/data"),              // data dir root
		loc("/data/secrets-run"),  // data child outside logs
		loc("/data/secrets"),      // data child outside logs
		loc("/home/u"),            // protected itself
		loc("/home/u/.gnupg/log"), // under a key store
		loc("/srv/logs"),          // contains a protected path
		loc("/srv"),               // ancestor of a protected path
		loc("/var/log/gordon"),
	}, protected, "/data")
	assert.Equal(t, []string{"/data/logs", "/data/logs/containers", "/var/log/gordon"}, readable)
	assert.Equal(t, []string{
		"/data/x.log", "/data/secrets-run/x.log", "/data/secrets/x.log", "/home/u/x.log",
		"/home/u/.gnupg/log/x.log", "/srv/logs/x.log", "/srv/x.log",
	}, keep)
}

func TestBuildAppRuntimeSafety_DroppedLogStaysProtected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	si := &serviceInit{}
	si.cfg.Logging.File.Enabled = true
	si.cfg.Logging.File.Path = filepath.Join(home, "gordon.log")
	store, guard, err := buildAppRuntimeSafety(si, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.(*secretfiles.Store).Close() })

	// $HOME contains key stores, so it is no readable log dir, and the
	// log file itself stays protected even against a writable bind.
	assert.NotContains(t, guard.LogDirs, home)
	err = guard.Check(&domain.ContainerConfig{Binds: []domain.ContainerBind{{Name: "log", Source: filepath.Join(home, "gordon.log"), Destination: "/log"}}})
	require.ErrorIs(t, err, domain.ErrRuntimeGuard)
}

func TestBuildAppRuntimeSafety(t *testing.T) {
	dataDir := t.TempDir()
	si := &serviceInit{}
	si.cfg.Logging.ContainerLogs.Enabled = true
	si.cfg.Logging.File.Enabled = true
	store, guard, err := buildAppRuntimeSafety(si, dataDir)
	require.NoError(t, err)
	require.NotNil(t, store)
	t.Cleanup(func() { _ = store.(*secretfiles.Store).Close() })
	assert.Equal(t, filepath.Join(dataDir, "secrets-run"), guard.SecretFilesDir)
	assert.Contains(t, guard.ProtectedPaths, dataDir)

	// The data dir itself cannot be bound into an app.
	err = guard.Check(&domain.ContainerConfig{Binds: []domain.ContainerBind{{Name: "all", Source: dataDir, Destination: "/data"}}})
	require.ErrorIs(t, err, domain.ErrRuntimeGuard)

	// The default log dir under the data dir is readable, never writable.
	logs := filepath.Join(dataDir, "logs")
	require.NoError(t, guard.Check(&domain.ContainerConfig{Binds: []domain.ContainerBind{{Name: "logs", Source: logs, Destination: "/var/log/gordon", ReadOnly: true}}}))
	err = guard.Check(&domain.ContainerConfig{Binds: []domain.ContainerBind{{Name: "logs", Source: logs, Destination: "/var/log/gordon"}}})
	require.ErrorIs(t, err, domain.ErrRuntimeGuard)
}
