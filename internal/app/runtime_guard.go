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
		configHome = subPath(home, ".config")
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
		subPath(home, ".gordon", "run"),
		dockerHost,
		subPath(home, ".local", "share", "containers"),
		subPath(home, ".local", "share", "docker"),
		subPath(configHome, "containers"),
		subPath(home, ".docker"),
		subPath(configHome, "systemd", "user"),
		getenv("GORDON_CLOUDFLARE_API_TOKEN_FILE"),
		getenv("GNUPGHOME"),
		subPath(home, ".gnupg"),
		getenv("PASSWORD_STORE_DIR"),
		subPath(home, ".password-store"),
		getenv("SOPS_AGE_KEY_FILE"),
		subPath(configHome, "sops", "age"),
	}, gordonPaths...)
	return absWithResolved(raw)
}

// absWithResolved makes each non-empty path absolute, adds its
// symlink-resolved form when it differs, and returns the sorted set.
func absWithResolved(raw []string) []string {
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

// configuredGordonPaths returns the config file, the runtime socket in use,
// and every private file or directory the config points Gordon at. Log
// paths are left to gordonLogDirs. The public TLS certificate is left out
// so its directory (often a shared CA bundle) stays bindable.
func configuredGordonPaths(si *serviceInit) []string {
	cfg := si.cfg
	paths := []string{
		cfg.Server.TLSKeyFile,
		cfg.Backups.StorageDir,
		cfg.Backups.Databases.StorageDir,
	}
	if si.v != nil {
		paths = append(paths, si.v.ConfigFileUsed(), resolveRuntimeConfig(si.v.GetString("server.runtime")))
	}
	return paths
}

// readableLogDirs keeps the log dirs that lie under the data dir or
// outside every protected path, so a log dir misconfigured inside a key
// store or runtime dir never opens a read-only exception there.
func readableLogDirs(logDirs, protected []string, dataDir string) []string {
	dataDirs := absWithResolved([]string{dataDir})
	var kept []string
	for _, logDir := range logDirs {
		ok := true
		for _, p := range protected {
			if slices.Contains(dataDirs, p) {
				continue
			}
			if pathWithin(p, logDir) || pathWithin(logDir, p) {
				ok = false
				break
			}
		}
		if ok {
			kept = append(kept, logDir)
		}
	}
	return kept
}

// pathWithin reports whether target is root or lies under it.
func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gordonLogDirs returns the directories of Gordon's process log, access
// log, and container logs. App binds may read them but never write them.
func gordonLogDirs(cfg Config, dataDir string) []string {
	containerLogs := cfg.Logging.ContainerLogs.Dir
	if containerLogs == "" {
		containerLogs = filepath.Join(dataDir, "logs", "containers")
	}
	accessLog := cfg.Logging.AccessLog.FilePath
	if accessLog == "" {
		accessLog = filepath.Join(dataDir, "logs", "access.log")
	}
	return absWithResolved([]string{
		filepath.Dir(resolveLogFilePath(cfg)),
		filepath.Dir(accessLog),
		containerLogs,
	})
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
	return store, domain.RuntimeGuard{
		ProtectedPaths: paths,
		SecretFilesDir: store.Dir(),
		LogDirs:        readableLogDirs(gordonLogDirs(si.cfg, dataDir), paths, dataDir),
	}, nil
}
