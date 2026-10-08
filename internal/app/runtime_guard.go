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
// under, or contain: Gordon's data and config, runtime sockets, Gordon's
// runtime credentials, and key stores. Empty entries are skipped; each
// entry is cleaned and its symlink-resolved form is added when it differs.
func protectedRuntimePaths(dataDir, configFile, runtimeSocket string, getenv func(string) string, home string) []string {
	runtimeDir := getenv("XDG_RUNTIME_DIR")
	configHome := getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = homePath(home, ".config")
	}
	dockerHost, _ := strings.CutPrefix(getenv("DOCKER_HOST"), "unix://")
	if strings.Contains(dockerHost, "://") {
		dockerHost = ""
	}
	raw := []string{
		dataDir,
		configFile,
		"/var/run/docker.sock",
		"/run/docker.sock",
		"/run/podman/podman.sock",
		subPath(runtimeDir, "docker.sock"),
		subPath(runtimeDir, "podman", "podman.sock"),
		subPath(runtimeDir, "gordon"),
		homePath(home, ".gordon", "run"),
		dockerHost,
		runtimeSocket,
		getenv("GNUPGHOME"),
		homePath(home, ".gnupg"),
		getenv("PASSWORD_STORE_DIR"),
		homePath(home, ".password-store"),
		getenv("SOPS_AGE_KEY_FILE"),
		subPath(configHome, "sops", "age"),
	}
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
		if resolved, err := filepath.EvalSymlinks(clean); err == nil && resolved != clean {
			paths = append(paths, resolved)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
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

// buildAppRuntimeSafety opens the host secret file store and builds the
// runtime guard applied to every app container create.
func buildAppRuntimeSafety(si *serviceInit, dataDir string) (out.SecretFileStore, domain.RuntimeGuard, error) {
	store, err := secretfiles.NewStore(dataDir)
	if err != nil {
		return nil, domain.RuntimeGuard{}, err
	}
	home, _ := os.UserHomeDir()
	var configFile, runtimeSocket string
	if si.v != nil {
		configFile = si.v.ConfigFileUsed()
		runtimeSocket = resolveRuntimeConfig(si.v.GetString("server.runtime"))
	}
	paths := protectedRuntimePaths(dataDir, configFile, runtimeSocket, os.Getenv, home)
	return store, domain.RuntimeGuard{ProtectedPaths: paths, SecretFilesDir: store.Dir()}, nil
}
