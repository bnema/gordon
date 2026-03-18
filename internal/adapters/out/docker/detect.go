package docker

import (
	"net"
	"os"
	"strings"
	"time"
)

// DetectionResult holds the result of runtime socket detection.
type DetectionResult struct {
	SocketPath  string
	Source      string
	RuntimeName string
}

var defaultSocketPaths = []string{
	"/var/run/docker.sock",
	"/run/podman/podman.sock",
}

// DetectRuntimeSocket detects the container runtime socket to use.
// If explicit is non-empty, it is used directly. Otherwise, DOCKER_HOST is
// checked, then well-known socket paths are probed.
func DetectRuntimeSocket(explicit string) DetectionResult {
	if explicit != "" {
		return DetectionResult{
			SocketPath:  explicit,
			Source:      "explicit",
			RuntimeName: guessRuntimeName(explicit),
		}
	}

	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost != "" {
		if path, ok := parseUnixSocketPath(dockerHost); ok {
			return DetectionResult{
				SocketPath:  path,
				Source:      "DOCKER_HOST",
				RuntimeName: guessRuntimeName(path),
			}
		}
		return DetectionResult{
			Source: "DOCKER_HOST_passthrough",
		}
	}

	for _, path := range defaultSocketPaths {
		if socketResponds(path) {
			return DetectionResult{
				SocketPath:  path,
				Source:      "probed",
				RuntimeName: guessRuntimeName(path),
			}
		}
	}

	xdg := os.Getenv("XDG_RUNTIME_DIR")
	if xdg != "" {
		podmanPath := xdg + "/podman/podman.sock"
		if socketResponds(podmanPath) {
			return DetectionResult{
				SocketPath:  podmanPath,
				Source:      "probed",
				RuntimeName: "podman",
			}
		}
	}

	return DetectionResult{Source: "none"}
}

func parseUnixSocketPath(value string) (string, bool) {
	if strings.HasPrefix(value, "unix://") {
		return strings.TrimPrefix(value, "unix://"), true
	}
	if strings.HasPrefix(value, "/") {
		return value, true
	}
	return "", false
}

func socketResponds(path string) bool {
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func guessRuntimeName(path string) string {
	if strings.Contains(path, "podman") {
		return "podman"
	}
	return "docker"
}
