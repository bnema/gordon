package container

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"

	"gordon/internal/config"
	"gordon/pkg/runtime"
)

// RuntimeType represents the container runtime type
type RuntimeType string

const (
	RuntimeAuto          RuntimeType = "auto"
	RuntimeDocker        RuntimeType = "docker"
	RuntimePodman        RuntimeType = "podman"
	RuntimePodmanRootless RuntimeType = "podman-rootless"
)

// RuntimeInfo contains information about a detected runtime
type RuntimeInfo struct {
	Type       RuntimeType
	SocketPath string
	Version    string
}

// CreateRuntime creates a container runtime based on configuration
func CreateRuntime(cfg *config.Config) (runtime.Runtime, error) {
	runtimeType := RuntimeType(cfg.Server.Runtime)
	
	// Get socket path from config or environment
	socketPath := getSocketPath(cfg.Server.SocketPath)
	
	switch runtimeType {
	case RuntimeAuto:
		return createAutoRuntime(socketPath)
	case RuntimeDocker:
		return createDockerRuntime(socketPath)
	case RuntimePodman, RuntimePodmanRootless:
		return createPodmanRuntime(socketPath, runtimeType == RuntimePodmanRootless)
	default:
		return nil, fmt.Errorf("unsupported runtime: %s", runtimeType)
	}
}

// getSocketPath determines the socket path to use
func getSocketPath(configPath string) string {
	// 1. Use config if provided
	if configPath != "" {
		log.Debug().Str("path", configPath).Msg("Using socket path from config")
		return configPath
	}
	
	// 2. Check CONTAINER_HOST environment variable
	if envPath := os.Getenv("CONTAINER_HOST"); envPath != "" {
		log.Debug().Str("path", envPath).Msg("Using socket path from CONTAINER_HOST")
		return envPath
	}
	
	// 3. Return empty string for auto-detection
	return ""
}

// createAutoRuntime automatically detects and creates the best available runtime
func createAutoRuntime(customSocketPath string) (runtime.Runtime, error) {
	var candidatePaths []string
	
	if customSocketPath != "" {
		// If custom path provided, try it first
		candidatePaths = []string{customSocketPath}
	} else {
		// Auto-detect socket paths
		candidatePaths = getDefaultSocketPaths()
	}
	
	// Try each socket path
	for _, socketPath := range candidatePaths {
		if rt, info, err := tryCreateRuntime(socketPath); err == nil {
			log.Info().
				Str("runtime", string(info.Type)).
				Str("socket", info.SocketPath).
				Str("version", info.Version).
				Msg("Container runtime auto-detected")
			return rt, nil
		}
	}
	
	return nil, fmt.Errorf("no container runtime found. Tried paths: %v", candidatePaths)
}

// createDockerRuntime creates a Docker runtime with optional custom socket
func createDockerRuntime(socketPath string) (runtime.Runtime, error) {
	var opts []client.Opt
	opts = append(opts, client.FromEnv, client.WithAPIVersionNegotiation())
	
	if socketPath != "" {
		opts = append(opts, client.WithHost(socketPath))
	}
	
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}
	
	// Test connectivity
	ctx := context.Background()
	if _, err := cli.Ping(ctx); err != nil {
		return nil, fmt.Errorf("Docker ping failed: %w", err)
	}
	
	return &DockerRuntime{client: cli}, nil
}

// createPodmanRuntime creates a Podman runtime with optional custom socket
func createPodmanRuntime(socketPath string, rootless bool) (runtime.Runtime, error) {
	if socketPath == "" {
		socketPath = getDefaultPodmanSocket(rootless)
	}
	
	return createPodmanRuntimeWithSocket(socketPath)
}

// tryCreateRuntime attempts to create a runtime for a given socket path
func tryCreateRuntime(socketPath string) (runtime.Runtime, *RuntimeInfo, error) {
	// Try to create client
	opts := []client.Opt{
		client.WithHost(socketPath),
		client.WithAPIVersionNegotiation(),
	}
	
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create client: %w", err)
	}
	
	// Test connectivity and get version
	ctx := context.Background()
	ping, err := cli.Ping(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("ping failed: %w", err)
	}
	
	// Determine runtime type based on socket path and server info
	runtimeType := detectRuntimeType(socketPath, ping.APIVersion)
	
	var rt runtime.Runtime
	if runtimeType == RuntimeDocker {
		rt = &DockerRuntime{client: cli}
	} else {
		rt = &PodmanRuntime{client: cli}
	}
	
	info := &RuntimeInfo{
		Type:       runtimeType,
		SocketPath: socketPath,
		Version:    ping.APIVersion,
	}
	
	return rt, info, nil
}

// detectRuntimeType determines if the socket is Docker or Podman
func detectRuntimeType(socketPath, apiVersion string) RuntimeType {
	// Check socket path patterns
	if strings.Contains(socketPath, "podman") {
		if strings.Contains(socketPath, "/run/user/") || strings.Contains(socketPath, "$XDG_RUNTIME_DIR") {
			return RuntimePodmanRootless
		}
		return RuntimePodman
	}
	
	if strings.Contains(socketPath, "docker") {
		return RuntimeDocker
	}
	
	// Default socket paths
	switch socketPath {
	case "/var/run/docker.sock", "unix:///var/run/docker.sock":
		return RuntimeDocker
	case "/run/podman/podman.sock", "unix:///run/podman/podman.sock":
		return RuntimePodman
	}
	
	// Check if it's in user runtime directory (rootless)
	if strings.Contains(socketPath, "/run/user/") {
		return RuntimePodmanRootless
	}
	
	// Default to Docker for unknown paths
	return RuntimeDocker
}

// getDefaultSocketPaths returns the default socket paths to try
func getDefaultSocketPaths() []string {
	paths := []string{
		"unix:///var/run/docker.sock",      // Docker
		"unix:///run/podman/podman.sock",   // Podman root
	}
	
	// Add Podman rootless socket
	if rootlessSocket := getDefaultPodmanSocket(true); rootlessSocket != "" {
		paths = append(paths, rootlessSocket)
	}
	
	return paths
}

// getDefaultPodmanSocket returns the default Podman socket path
func getDefaultPodmanSocket(rootless bool) string {
	if !rootless {
		return "unix:///run/podman/podman.sock"
	}
	
	// Get user runtime directory for rootless Podman
	if xdgRuntimeDir := os.Getenv("XDG_RUNTIME_DIR"); xdgRuntimeDir != "" {
		return fmt.Sprintf("unix://%s/podman/podman.sock", xdgRuntimeDir)
	}
	
	// Fallback: construct from user ID
	if currentUser, err := user.Current(); err == nil {
		return fmt.Sprintf("unix:///run/user/%s/podman/podman.sock", currentUser.Uid)
	}
	
	log.Warn().Msg("Could not determine rootless Podman socket path")
	return ""
}

// createPodmanRuntimeWithSocket creates a Podman runtime for a specific socket
func createPodmanRuntimeWithSocket(socketPath string) (runtime.Runtime, error) {
	opts := []client.Opt{
		client.WithHost(socketPath),
		client.WithAPIVersionNegotiation(),
	}
	
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Podman client: %w", err)
	}
	
	// Test connectivity
	ctx := context.Background()
	if _, err := cli.Ping(ctx); err != nil {
		return nil, fmt.Errorf("Podman ping failed: %w", err)
	}
	
	return &PodmanRuntime{client: cli}, nil
}

// GetAvailableRuntimes returns information about all available runtimes
func GetAvailableRuntimes() []RuntimeInfo {
	var runtimes []RuntimeInfo
	
	for _, socketPath := range getDefaultSocketPaths() {
		if _, info, err := tryCreateRuntime(socketPath); err == nil {
			runtimes = append(runtimes, *info)
		}
	}
	
	return runtimes
}