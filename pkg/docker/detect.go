package docker

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/bnema/gordon/pkg/logger"
)

func fileExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return !os.IsNotExist(err)
}

func IsRunningInContainer() bool {
	// Method 1: Check for .dockerenv file or .iscontainer (Docker standard)
	if fileExists("/.iscontainer") || fileExists("/.dockerenv") {
		logger.Debug("IsRunningInContainer: Container detected via marker files")
		return true
	}
	
	// Method 2: Check hostname format (often container IDs in Docker/Podman)
	hostname := os.Getenv("HOSTNAME")
	if hostname != "" && len(hostname) >= 12 {
		// Container IDs are typically at least 12 chars and hexadecimal
		isHexString := true
		for _, c := range hostname[:12] {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				isHexString = false
				break
			}
		}
		
		if isHexString {
			logger.Debug("IsRunningInContainer: Container detected via hostname format", "hostname", hostname)
			return true
		}
	}
	
	// Method 3: Check cgroup info (works in most container environments)
	if cgroupData, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		if !strings.Contains(string(cgroupData), ":/") {
			// Non-root cgroup namespace indicates containerization
			logger.Debug("IsRunningInContainer: Container detected via cgroup namespace")
			return true
		}
	}
	
	logger.Debug("IsRunningInContainer: Not running in a container")
	return false
}

// DetectPodman automatically detects if Podman is available and returns
// true along with the appropriate socket path if found
func DetectPodman() (bool, string) {
	// Check system socket locations first (root installs)
	systemSockets := []string{
		"/run/podman/podman.sock",
		"/var/run/podman/podman.sock",
	}

	for _, socket := range systemSockets {
		if fileExists(socket) {
			logger.Debug("DetectPodman: Found system Podman socket", "path", socket)
			return true, socket
		}
	}

	// Then check for rootless installation
	currentUser, err := user.Current()
	if err != nil {
		logger.Error("DetectPodman: Failed to get current user", "error", err)
		return false, ""
	}

	// Check XDG_RUNTIME_DIR based socket (common for rootless)
	xdgRuntime := os.Getenv("XDG_RUNTIME_DIR")
	if xdgRuntime != "" {
		xdgSocket := filepath.Join(xdgRuntime, "podman", "podman.sock")
		if fileExists(xdgSocket) {
			logger.Debug("DetectPodman: Found rootless Podman socket (XDG)", "path", xdgSocket)
			return true, xdgSocket
		}
	}

	// Check home directory socket location
	homeSocket := filepath.Join(currentUser.HomeDir, ".local", "share", "containers", "podman", "machine", "podman.sock")
	if fileExists(homeSocket) {
		logger.Debug("DetectPodman: Found rootless Podman socket (home)", "path", homeSocket)
		return true, homeSocket
	}

	// No Podman socket found
	logger.Debug("DetectPodman: No Podman sockets found")
	return false, ""
}
