package common

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/bnema/gordon/pkg/docker"
)

func DockerInit(cc *ContainerEngineConfig) error {
	// Auto-detect Podman based on socket path
	isPodmanSocket := detectPodmanSocket(cc.Sock)
	if isPodmanSocket && !cc.Podman {
		fmt.Println("Podman socket detected at", cc.Sock)
		fmt.Println("Automatically enabling Podman support")
		cc.Podman = true
		cc.PodmanSock = cc.Sock
	}

	config := NewDockerConfig(cc)
	err := docker.InitializeClient(config)
	if err != nil {
		// Check if it's a "socket doesn't exist" error
		if strings.Contains(err.Error(), "no such file or directory") {
			// Log the error
			log.Printf("Error initializing Docker client: %s", err)

			// Try alternative socket paths
			socketPaths := []string{
				"/run/podman/podman.sock",                        // Standard path
				"/var/run/podman/podman.sock",                    // Alternative standard path
				os.Getenv("HOME") + "/.local/podman/podman.sock", // Some distros
				"/run/user/1000/podman/podman.sock",              // User-specific socket
			}

			// Add Docker socket path as a fallback
			if !isPodmanSocket {
				socketPaths = append(socketPaths, "/var/run/docker.sock")
			}

			for _, path := range socketPaths {
				// Skip if it's the path we already tried
				if path == cc.Sock {
					continue
				}

				// Check if this socket exists
				if _, err := os.Stat(path); err == nil {
					log.Printf("Found alternative socket at %s", path)

					// Detect if this is a Podman socket
					isPodmanSocket = detectPodmanSocket(path)
					if isPodmanSocket {
						cc.Podman = true
						cc.PodmanSock = path
					}

					cc.Sock = path

					// Try again with the new path
					newConfig := NewDockerConfig(cc)
					if err := docker.InitializeClient(newConfig); err == nil {
						return nil
					}
				}
			}

			// Return without an error, as we want to continue even if Docker is not available
			log.Printf("Docker/Podman functionality will be limited or unavailable")
			return fmt.Errorf("could not connect to container engine: %w", err)
		}

		return err
	}
	return nil
}

// detectPodmanSocket checks if a socket path is likely a Podman socket
func detectPodmanSocket(socketPath string) bool {
	if socketPath == "" {
		return false
	}

	// Check if it's a user-specific Podman socket path
	if strings.Contains(socketPath, "user") && strings.Contains(socketPath, "podman") {
		log.Printf("User-specific rootless Podman socket detected: %s", socketPath)
		return true
	}

	return strings.Contains(socketPath, "podman")
}

// NewDockerConfig creates and returns a new Docker client configuration
func NewDockerConfig(cc *ContainerEngineConfig) *docker.Config {
	if cc.Podman {
		return &docker.Config{
			Sock:         cc.PodmanSock,
			PodmanEnable: true,
		}
	}
	return &docker.Config{
		Sock:         cc.Sock,
		PodmanEnable: false,
	}
}
