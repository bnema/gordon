package domain

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RuntimeGuard is the last check on every app container config before it
// reaches the runtime. It refuses configs that would give a workload
// access to the host or to Gordon itself, whatever produced them.
//
// The zero value still refuses host/shared network namespaces and host
// paths passed as volume names; ProtectedPaths adds the bind checks.
type RuntimeGuard struct {
	// ProtectedPaths are absolute host paths no bind may equal, lie under,
	// or contain (data dir, config, runtime sockets, key stores).
	ProtectedPaths []string
	// SecretFilesDir is the host base directory of file-delivered secrets
	// (<data_dir>/secrets-run). Gordon's own read-only secret binds under
	// it are the only allowed exception.
	SecretFilesDir string
	// LogDirs are Gordon's log directories. Binds overlapping them are
	// refused unless read-only; a read-only bind at or under one is
	// allowed even when a protected path (the data dir) contains it, so
	// a log shipper can read them. A log dir that is or contains a
	// protected path grants no exception.
	LogDirs []string
}

// Check refuses a config that breaks the guard. Errors wrap
// ErrRuntimeGuard and name the bind, volume destination, or network mode,
// never a host path.
func (g RuntimeGuard) Check(config *ContainerConfig) error {
	if err := checkGuardNetworkMode(config.NetworkMode); err != nil {
		return err
	}
	if err := checkGuardVolumes(config.Volumes); err != nil {
		return err
	}
	if err := checkGuardVolumes(config.ReadOnlyVolumes); err != nil {
		return err
	}
	for _, bind := range config.Binds {
		if g.isOwnSecretBind(config, bind) {
			continue
		}
		if g.overlapsProtected(bind) {
			return fmt.Errorf("%w: bind %q overlaps a protected Gordon path", ErrRuntimeGuard, bind.Name)
		}
	}
	return nil
}

func checkGuardNetworkMode(mode string) error {
	if mode == "host" || strings.HasPrefix(mode, "container:") || strings.HasPrefix(mode, "ns:") {
		return fmt.Errorf("%w: network mode %q is not allowed", ErrRuntimeGuard, guardNetworkLabel(mode))
	}
	return nil
}

// guardNetworkLabel keeps the mode kind but drops a namespace path.
func guardNetworkLabel(mode string) string {
	if kind, _, ok := strings.Cut(mode, ":"); ok {
		return kind + ":"
	}
	return mode
}

// checkGuardVolumes refuses volume names the runtime would read as host
// paths and turn into binds.
func checkGuardVolumes(volumes map[string]string) error {
	for dest, name := range volumes {
		if strings.ContainsAny(name, `/\`) {
			return fmt.Errorf("%w: volume for %q is not a named volume", ErrRuntimeGuard, dest)
		}
	}
	return nil
}

// isOwnSecretBind reports whether bind is exactly the read-only secret file
// mount Gordon creates: SecretFilesDir/<app>/<container>/<KEY> mounted at
// /run/secrets/<KEY>.
func (g RuntimeGuard) isOwnSecretBind(config *ContainerConfig, bind ContainerBind) bool {
	if g.SecretFilesDir == "" || !bind.ReadOnly {
		return false
	}
	app := config.Labels[LabelApp]
	if app == "" || config.Name == "" {
		return false
	}
	key := filepath.Base(bind.Source)
	if err := ValidateEnvKey(key); err != nil {
		return false
	}
	return bind.Source == filepath.Join(g.SecretFilesDir, app, config.Name, key) &&
		bind.Destination == SecretFilePath(key)
}

// overlapsProtected reports whether the bind source, raw or
// symlink-resolved, is equal to, under, or an ancestor of a protected
// path, or of a log dir when the bind is writable. A read-only source at
// or under a usable log dir only fails when it contains a protected path.
func (g RuntimeGuard) overlapsProtected(bind ContainerBind) bool {
	for _, candidate := range guardCandidates(bind.Source) {
		readableLog := bind.ReadOnly && g.underUsableLogDir(candidate)
		for _, protected := range g.ProtectedPaths {
			if resolvedUnderRoot(candidate, protected) {
				return true
			}
			if !readableLog && resolvedUnderRoot(protected, candidate) {
				return true
			}
		}
		if bind.ReadOnly {
			continue
		}
		for _, logDir := range g.LogDirs {
			if resolvedUnderRoot(logDir, candidate) || resolvedUnderRoot(candidate, logDir) {
				return true
			}
		}
	}
	return false
}

// underUsableLogDir reports whether path is at or under a log dir that
// neither is nor contains a protected path.
func (g RuntimeGuard) underUsableLogDir(path string) bool {
	for _, logDir := range g.LogDirs {
		if resolvedUnderRoot(logDir, path) && !g.containsProtected(logDir) {
			return true
		}
	}
	return false
}

func (g RuntimeGuard) containsProtected(dir string) bool {
	for _, protected := range g.ProtectedPaths {
		if resolvedUnderRoot(dir, protected) {
			return true
		}
	}
	return false
}

func guardCandidates(source string) []string {
	clean := filepath.Clean(source)
	candidates := []string{clean}
	if resolved, err := filepath.EvalSymlinks(clean); err == nil && resolved != clean {
		candidates = append(candidates, resolved)
	}
	return candidates
}
