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
// paths are left to gordonLogLocations. The public TLS certificate is left out
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

// logLocation is one enabled Gordon log: the directory a read-only bind
// may expose and the exact file or directory Gordon writes.
type logLocation struct {
	dir  string
	path string
}

// readableLogDirs splits the log locations into dirs that may be bound
// read-only and exact log paths that must stay protected instead. A dir
// under the data dir is readable only at or under <data_dir>/logs; a dir
// outside it only when it neither is, contains, nor lies under another
// protected path. So a misconfigured log path never opens a read-only
// exception on secrets, key stores, or runtime dirs, and a dropped log
// stays write-protected.
func readableLogDirs(locations []logLocation, protected []string, dataDir string) (readable, keepProtected []string) {
	dataDirs := absWithResolved([]string{dataDir})
	dataLogs := absWithResolved([]string{filepath.Join(dataDir, "logs")})
	for _, loc := range locations {
		if readableLogDir(loc.dir, protected, dataDirs, dataLogs) {
			readable = append(readable, loc.dir)
		} else {
			keepProtected = append(keepProtected, loc.path)
		}
	}
	return readable, keepProtected
}

func readableLogDir(dir string, protected, dataDirs, dataLogs []string) bool {
	underData := slices.ContainsFunc(dataDirs, func(d string) bool { return pathWithin(d, dir) })
	if underData && !slices.ContainsFunc(dataLogs, func(l string) bool { return pathWithin(l, dir) }) {
		return false
	}
	for _, p := range protected {
		if underData && slices.Contains(dataDirs, p) {
			continue
		}
		if pathWithin(p, dir) || pathWithin(dir, p) {
			return false
		}
	}
	return true
}

// pathWithin reports whether target is root or lies under it.
func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gordonLogLocations returns the enabled file logs: Gordon's process log,
// the access log, and the container logs dir, with paths made absolute.
func gordonLogLocations(cfg Config, dataDir string) []logLocation {
	var locs []logLocation
	add := func(dir, path string) {
		dir, errDir := filepath.Abs(dir)
		path, errPath := filepath.Abs(path)
		if errDir == nil && errPath == nil {
			locs = append(locs, logLocation{dir: dir, path: path})
		}
	}
	if cfg.Logging.File.Enabled {
		p := cfg.Logging.File.Path
		if p == "" {
			p = filepath.Join(dataDir, "logs", "gordon.log")
		}
		add(filepath.Dir(p), p)
	}
	if cfg.Logging.AccessLog.Enabled && cfg.Logging.AccessLog.Output == "file" {
		p := cfg.Logging.AccessLog.FilePath
		if p == "" {
			p = filepath.Join(dataDir, "logs", "access.log")
		}
		add(filepath.Dir(p), p)
	}
	if cfg.Logging.ContainerLogs.Enabled {
		p := cfg.Logging.ContainerLogs.Dir
		if p == "" {
			p = filepath.Join(dataDir, "logs", "containers")
		}
		add(p, p)
	}
	return locs
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
	logDirs, logPaths := readableLogDirs(gordonLogLocations(si.cfg, dataDir), paths, dataDir)
	paths = absWithResolved(append(paths, logPaths...))
	return store, domain.RuntimeGuard{
		ProtectedPaths: paths,
		SecretFilesDir: store.Dir(),
		LogDirs:        absWithResolved(logDirs),
	}, nil
}
