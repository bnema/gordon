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

	// If socket is empty, try to detect it automatically
	if cc.Sock == "" {
		// Try standard paths first
		standardPaths := []string{
			"/var/run/docker.sock",        // Standard Docker socket in containers
			"/run/podman/podman.sock",     // Standard Podman path
			"/var/run/podman/podman.sock", // Alternative standard path
		}

		// Add home directory based path
		homePath := os.Getenv("HOME")
		if homePath != "" {
			standardPaths = append(standardPaths, homePath+"/.local/podman/podman.sock")
		}

		// Check standard paths first
		for _, path := range standardPaths {
			if _, err := os.Stat(path); err == nil {
				log.Printf("Auto-detected container engine socket at %s", path)

				// Detect if this is a Podman socket
				isPodmanSocket = detectPodmanSocket(path)
				if isPodmanSocket {
					cc.Podman = true
					cc.PodmanSock = path
				}

				cc.Sock = path
				break
			} else {
				log.Printf("Socket not found at: %s", path)
			}
		}

		// If not found in standard paths, try common user IDs
		if cc.Sock == "" {
			log.Printf("Scanning for user-specific Podman sockets in ID range 1000-1100...")

			// Try common user IDs from 1000 to 1100 (typical range for first users on Linux)
			for uid := 1000; uid <= 1100; uid++ {
				uidStr := fmt.Sprintf("%d", uid)
				podmanSocketPath := fmt.Sprintf("/run/user/%s/podman/podman.sock", uidStr)
				dockerSocketPath := fmt.Sprintf("/run/user/%s/docker/docker.sock", uidStr)

				// Check the Podman socket exists
				if _, err := os.Stat(podmanSocketPath); err == nil {
					// Check if the socket is accessible for reading/writing
					if file, err := os.OpenFile(podmanSocketPath, os.O_RDWR, 0); err == nil {
						file.Close()
						log.Printf("Found accessible Podman socket for user ID %s at: %s", uidStr, podmanSocketPath)
						cc.Podman = true
						cc.PodmanSock = podmanSocketPath
						cc.Sock = podmanSocketPath
						break
					} else {
						log.Printf("Found Podman socket for user ID %s at %s but it's not accessible: %v", uidStr, podmanSocketPath, err)
					}
				}

				// Check Docker socket for this UID
				if _, err := os.Stat(dockerSocketPath); err == nil {
					if file, err := os.OpenFile(dockerSocketPath, os.O_RDWR, 0); err == nil {
						file.Close()
						log.Printf("Found accessible Docker socket for user ID %s at: %s", uidStr, dockerSocketPath)
						cc.Sock = dockerSocketPath
						break
					}
				}
			}
		}
	}

	// If we found a socket, try to initialize Docker client
	if cc.Sock != "" {
		config := NewDockerConfig(cc)
		err := docker.InitializeClient(config)
		if err == nil {
			log.Printf("Successfully initialized Docker client with socket: %s", cc.Sock)
			return nil
		}

		// Log the error but continue with fallback strategy
		log.Printf("Error initializing Docker client: %s", err)
	}

	// If we failed to find or initialize a socket, try a comprehensive fallback scan
	// across all common socket locations and user IDs
	socketPaths := []string{
		"/var/run/docker.sock",
		"/run/podman/podman.sock",
		"/var/run/podman/podman.sock",
	}

	// Add home directory based path
	homePath := os.Getenv("HOME")
	if homePath != "" {
		socketPaths = append(socketPaths, homePath+"/.local/podman/podman.sock")
	}

	// Skip if we already tried this path
	if cc.Sock != "" {
		skipPath := cc.Sock
		for i, path := range socketPaths {
			if path == skipPath {
				socketPaths = append(socketPaths[:i], socketPaths[i+1:]...)
				break
			}
		}
	}

	// Try common user IDs from 1000 to 1100
	for uid := 1000; uid <= 1100; uid++ {
		uidStr := fmt.Sprintf("%d", uid)
		podmanSocketPath := fmt.Sprintf("/run/user/%s/podman/podman.sock", uidStr)
		dockerSocketPath := fmt.Sprintf("/run/user/%s/docker/docker.sock", uidStr)

		// Check the Podman socket exists
		if _, err := os.Stat(podmanSocketPath); err == nil {
			// Check if the socket is accessible for reading/writing
			if file, err := os.OpenFile(podmanSocketPath, os.O_RDWR, 0); err == nil {
				file.Close()
				log.Printf("Found accessible Podman socket for user ID %s at: %s", uidStr, podmanSocketPath)
				socketPaths = append([]string{podmanSocketPath}, socketPaths...) // Prioritize this path
			} else {
				log.Printf("Found Podman socket for user ID %s at %s but it's not accessible: %v", uidStr, podmanSocketPath, err)
			}
		}

		// Also check Docker socket for this UID
		if _, err := os.Stat(dockerSocketPath); err == nil {
			socketPaths = append(socketPaths, dockerSocketPath)
		}
	}

	log.Printf("Comprehensive socket scan across %d potential paths...", len(socketPaths))

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
				log.Printf("Successfully initialized Docker client with socket: %s", path)
				return nil
			} else {
				log.Printf("Socket found but client initialization failed: %s", err)
			}
		}
	}

	// If we still haven't found a valid socket, give up but don't fail the application
	log.Printf("No working container engine socket found after comprehensive scan")
	log.Printf("Docker/Podman functionality will be limited or unavailable")
	return fmt.Errorf("could not find or connect to any container engine socket")
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
