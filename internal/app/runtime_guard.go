package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bnema/gordon/internal/adapters/out/secretfiles"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// protectedRuntimePaths lists the host paths no app bind may equal, lie
// under, or contain: Gordon's data, config, and configured files, runtime
// sockets and storage, Gordon's runtime credentials, and key stores.
// Empty entries are skipped; each entry is made absolute and its
// symlink-resolved form is added when it differs.
func protectedRuntimePaths(dataDir string, gordonPaths []string, getenv func(string) string, home string) []string {
	runtimeDir := getenv("XDG_RUNTIME_DIR")
	configHome := getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = homePath(home, ".config")
	}
	dockerHost, _ := strings.CutPrefix(getenv("DOCKER_HOST"), "unix://")
	if strings.Contains(dockerHost, "://") {
		dockerHost = ""
	}
	raw := append([]string{
		dataDir,
		"/var/run/docker.sock",
		"/run/docker.sock",
		"/run/podman/podman.sock",
		subPath(runtimeDir, "docker.sock"),
		subPath(runtimeDir, "podman", "podman.sock"),
		subPath(runtimeDir, "gordon"),
		homePath(home, ".gordon", "run"),
		dockerHost,
		homePath(home, ".local", "share", "containers"),
		homePath(home, ".local", "share", "docker"),
		subPath(configHome, "containers"),
		homePath(home, ".docker"),
		subPath(configHome, "systemd", "user"),
		getenv("GORDON_CLOUDFLARE_API_TOKEN_FILE"),
		getenv("GNUPGHOME"),
		homePath(home, ".gnupg"),
		getenv("PASSWORD_STORE_DIR"),
		homePath(home, ".password-store"),
		getenv("SOPS_AGE_KEY_FILE"),
		subPath(configHome, "sops", "age"),
	}, gordonPaths...)
	var paths []string
	for _, p := range raw {
		if p == "" {
			continue
		}
		// A relative data dir or config path is resolved against the
		// working directory, as the daemon itself resolves it.
		clean, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		paths = append(paths, clean)
		if resolved := resolveExisting(clean); resolved != clean {
			paths = append(paths, resolved)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// resolveExisting resolves symlinks in the longest existing ancestor of p
// and re-appends the missing tail, so a path created after startup under
// a symlinked parent (for example /home -> /var/home) is still protected
// in its resolved form.
func resolveExisting(p string) string {
	tail := ""
	for dir := p; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, tail)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		tail = filepath.Join(filepath.Base(dir), tail)
	}
}

// subPath joins elems under base, or returns "" when base is empty.
func subPath(base string, elems ...string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, elems...)...)
}

// homePath joins elems under the home directory, or returns "" when the
// home directory is unknown.
func homePath(home string, elems ...string) string {
	return subPath(home, elems...)
}

// configuredGordonPaths returns the config file, the runtime socket in use,
// and every file or directory the config points Gordon at.
func configuredGordonPaths(si *serviceInit) []string {
	cfg := si.cfg
	paths := []string{
		cfg.Server.TLSCertFile,
		cfg.Server.TLSKeyFile,
		resolveLogFilePath(cfg),
		cfg.Logging.ContainerLogs.Dir,
		cfg.Logging.AccessLog.FilePath,
		cfg.Backups.StorageDir,
		cfg.Backups.Databases.StorageDir,
	}
	if si.v != nil {
		paths = append(paths, si.v.ConfigFileUsed(), resolveRuntimeConfig(si.v.GetString("server.runtime")))
	}
	return paths
}

// buildAppRuntimeSafety opens the host secret file store and builds the
// runtime guard applied to every app container create. The store keeps one
// directory handle open for the daemon's lifetime.
func buildAppRuntimeSafety(si *serviceInit, dataDir string) (out.SecretFileStore, domain.RuntimeGuard, error) {
	store, err := secretfiles.NewStore(dataDir)
	if err != nil {
		return nil, domain.RuntimeGuard{}, err
	}
	home, _ := os.UserHomeDir()
	paths := protectedRuntimePaths(dataDir, configuredGordonPaths(si), os.Getenv, home)
	return store, domain.RuntimeGuard{ProtectedPaths: paths, SecretFilesDir: store.Dir()}, nil
}
