package docker

import (
	"os"
	"os/user"
	"path/filepath"

	log "github.com/charmbracelet/log"
)

func fileExists(filepath string) bool {
	_, err := os.Stat(filepath)
	return !os.IsNotExist(err)
}

func IsRunningInContainer() bool {

	// Check for .dockerenv file or .iscontainer
	isContainer := fileExists("/.iscontainer") || fileExists("/.dockerenv")
	log.Debug("IsRunningInContainer: Container detection result", "isContainer", isContainer)
	return isContainer
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
			log.Debug("DetectPodman: Found system Podman socket", "path", socket)
			return true, socket
		}
	}

	// Then check for rootless installation
	currentUser, err := user.Current()
	if err != nil {
		log.Error("DetectPodman: Failed to get current user", "error", err)
		return false, ""
	}

	// Check XDG_RUNTIME_DIR based socket (common for rootless)
	xdgRuntime := os.Getenv("XDG_RUNTIME_DIR")
	if xdgRuntime != "" {
		xdgSocket := filepath.Join(xdgRuntime, "podman", "podman.sock")
		if fileExists(xdgSocket) {
			log.Debug("DetectPodman: Found rootless Podman socket (XDG)", "path", xdgSocket)
			return true, xdgSocket
		}
	}

	// Check home directory socket location
	homeSocket := filepath.Join(currentUser.HomeDir, ".local", "share", "containers", "podman", "machine", "podman.sock")
	if fileExists(homeSocket) {
		log.Debug("DetectPodman: Found rootless Podman socket (home)", "path", homeSocket)
		return true, homeSocket
	}

	// No Podman socket found
	log.Debug("DetectPodman: No Podman sockets found")
	return false, ""
}
