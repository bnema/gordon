package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/bnema/zerowrap"
)

// SendReloadSignal sends SIGUSR1 to the running Gordon process.
func SendReloadSignal() error {
	process, _, err := findRunningProcess()
	if err != nil {
		return err
	}

	if err := process.Signal(syscall.SIGUSR1); err != nil {
		return fmt.Errorf("failed to send reload signal: %w", err)
	}

	return nil
}

// createPidFile creates a PID file for the Gordon process.
// SECURITY: Prefers secure locations (XDG_RUNTIME_DIR, ~/.gordon/run) over /tmp
// to prevent symlink attacks and unauthorized access.
func createPidFile(log zerowrap.Logger) string {
	pid := os.Getpid()

	// SECURITY: Prioritize secure locations over /tmp
	var locations []string

	// Try secure runtime directory first
	if runtimeDir, err := getSecureRuntimeDir(); err == nil {
		locations = append(locations, filepath.Join(runtimeDir, "gordon.pid"))
	}

	// Fall back to home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		locations = append(locations, filepath.Join(homeDir, ".gordon", "gordon.pid"))
	}

	// Last resort: /tmp (least secure due to world-writable)
	locations = append(locations, filepath.Join(os.TempDir(), "gordon.pid"))

	for _, location := range locations {
		// Ensure parent directory exists with secure permissions
		if err := os.MkdirAll(filepath.Dir(location), 0700); err != nil {
			continue
		}
		if err := os.WriteFile(location, fmt.Appendf(nil, "%d", pid), 0600); err == nil {
			log.Debug().Str("pid_file", location).Int("pid", pid).Msg("created PID file")
			return location
		}
	}

	log.Warn().Int("pid", pid).Msg("failed to create PID file in any location")
	return ""
}

// removePidFile removes the PID file.
func removePidFile(pidFile string, log zerowrap.Logger) {
	if err := os.Remove(pidFile); err != nil {
		log.Warn().Err(err).Str("pid_file", pidFile).Msg("failed to remove PID file")
	} else {
		log.Debug().Str("pid_file", pidFile).Msg("removed PID file")
	}
}

func pidFileLocations() []string {
	var locations []string
	seen := make(map[string]struct{})
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		locations = append(locations, path)
	}

	// Check secure runtime directory first
	if runtimeDir, err := getSecureRuntimeDir(); err == nil {
		add(filepath.Join(runtimeDir, "gordon.pid"))
	}

	// Also check canonical /run/user/<uid> runtime path. This handles cases where
	// Gordon started under systemd user services with runtime dir available, but
	// CLI invocations (e.g. non-interactive SSH) don't have XDG_RUNTIME_DIR set.
	runtimeByUID := filepath.Join("/run/user", strconv.Itoa(os.Getuid()), "gordon", "gordon.pid")
	add(runtimeByUID)

	// Check explicit XDG_RUNTIME_DIR if present in this process env.
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		add(filepath.Join(runtimeDir, "gordon.pid"))
	}

	// Check home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(homeDir, ".gordon", "gordon.pid"))
		// Legacy location for backward compatibility
		add(filepath.Join(homeDir, ".gordon.pid"))
	}

	// Legacy /tmp locations for backward compatibility
	add(filepath.Join(os.TempDir(), "gordon.pid"))
	add("/tmp/gordon.pid")

	return locations
}

// findRunningPidFile returns the first PID file whose PID belongs to a live process.
// Stale/invalid PID files are ignored and removed when possible.
func findRunningPidFile() (string, int, error) {
	return findRunningPidFileInLocations(pidFileLocations())
}

func findRunningPidFileInLocations(locations []string) (string, int, error) {
	foundAny := false

	for _, location := range locations {
		pidBytes, err := os.ReadFile(location)
		if err != nil {
			continue
		}

		foundAny = true

		var pid int
		if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil || pid <= 0 {
			_ = os.Remove(location)
			continue
		}

		if isProcessAlive(pid) {
			return location, pid, nil
		}

		_ = os.Remove(location)
	}

	if foundAny {
		return "", 0, fmt.Errorf("found stale gordon PID file(s), is Gordon running?")
	}

	return "", 0, fmt.Errorf("gordon PID file not found, is Gordon running?")
}

func findRunningProcess() (*os.Process, int, error) {
	_, pid, err := findRunningPidFile()
	if err != nil {
		return nil, 0, err
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to find process: %w", err)
	}

	return process, pid, nil
}

func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}

	return errors.Is(err, syscall.EPERM)
}
