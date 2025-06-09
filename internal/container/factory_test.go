package container

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gordon/internal/config"
)

func TestCreateRuntime(t *testing.T) {
	ctx := context.Background()
	
	tests := []struct {
		name           string
		config         *config.Config
		expectError    bool
		expectedError  string
	}{
		{
			name: "docker runtime",
			config: &config.Config{
				Server: config.ServerConfig{
					Runtime:    "docker",
					SocketPath: "/var/run/docker.sock",
				},
			},
			expectError: true, // Will fail because Docker isn't running in test
		},
		{
			name: "podman runtime",
			config: &config.Config{
				Server: config.ServerConfig{
					Runtime:    "podman",
					SocketPath: "/run/user/1000/podman/podman.sock",
				},
			},
			expectError: true, // Will fail because Podman isn't running in test
		},
		{
			name: "auto runtime",
			config: &config.Config{
				Server: config.ServerConfig{
					Runtime: "auto",
				},
			},
			expectError: false, // May succeed if runtime is available
		},
		{
			name: "invalid runtime",
			config: &config.Config{
				Server: config.ServerConfig{
					Runtime: "invalid",
				},
			},
			expectError:   true,
			expectedError: "unsupported runtime",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, err := CreateRuntime(ctx, tt.config)
			
			if tt.name == "auto runtime" {
				// Auto runtime may succeed or fail depending on environment
				if err != nil {
					assert.Nil(t, runtime)
				} else {
					assert.NotNil(t, runtime)
				}
			} else if tt.expectError {
				assert.Error(t, err)
				if tt.expectedError != "" {
					assert.Contains(t, err.Error(), tt.expectedError)
				}
				assert.Nil(t, runtime)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, runtime)
			}
		})
	}
}

func TestGetSocketPath(t *testing.T) {
	tests := []struct {
		name         string
		configPath   string
		setupEnv     func()
		cleanupEnv   func()
		expected     string
	}{
		{
			name:       "with provided config path",
			configPath: "/custom/docker.sock",
			setupEnv:   func() {},
			cleanupEnv: func() {},
			expected:   "/custom/docker.sock",
		},
		{
			name:       "with CONTAINER_HOST env var",
			configPath: "",
			setupEnv: func() {
				os.Setenv("CONTAINER_HOST", "/env/container.sock")
			},
			cleanupEnv: func() {
				os.Unsetenv("CONTAINER_HOST")
			},
			expected: "/env/container.sock",
		},
		{
			name:       "no config or env - returns empty",
			configPath: "",
			setupEnv: func() {
				os.Unsetenv("CONTAINER_HOST")
			},
			cleanupEnv: func() {},
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setupEnv()
			defer tt.cleanupEnv()
			
			result := getSocketPath(tt.configPath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestCreateAutoRuntime(t *testing.T) {
	ctx := context.Background()
	
	// Test that createAutoRuntime tries different runtimes
	runtime, err := createAutoRuntime(ctx, "")
	// In test environment, this may succeed or fail depending on available runtimes
	if err != nil {
		// If it fails, should be because no runtime is available
		assert.Nil(t, runtime)
		assert.Contains(t, err.Error(), "no container runtime available")
	} else {
		// If it succeeds, should return a runtime
		assert.NotNil(t, runtime)
	}
}

func TestCreateDockerRuntime(t *testing.T) {
	ctx := context.Background()
	
	tests := []struct {
		name        string
		socketPath  string
		expectError bool
	}{
		{
			name:        "default docker socket",
			socketPath:  "/var/run/docker.sock",
			expectError: true, // Will fail because Docker isn't running
		},
		{
			name:        "custom socket path",
			socketPath:  "/custom/docker.sock",
			expectError: true, // Will fail because socket doesn't exist
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, err := createDockerRuntime(ctx, tt.socketPath)
			
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, runtime)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, runtime)
			}
		})
	}
}

func TestCreatePodmanRuntime(t *testing.T) {
	ctx := context.Background()
	
	tests := []struct {
		name        string
		socketPath  string
		rootless    bool
		expectError bool
	}{
		{
			name:        "custom podman socket",
			socketPath:  "/custom/podman.sock",
			rootless:    false,
			expectError: true, // Will fail because socket doesn't exist
		},
		{
			name:        "rootless podman socket",
			socketPath:  "/custom/podman.sock", 
			rootless:    true,
			expectError: true, // Will fail because socket doesn't exist
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime, err := createPodmanRuntime(ctx, tt.socketPath, tt.rootless)
			
			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, runtime)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, runtime)
			}
		})
	}
}

func TestDetectRuntimeType(t *testing.T) {
	tests := []struct {
		name       string
		socketPath string
		expected   RuntimeType
	}{
		{
			name:       "docker socket",
			socketPath: "/var/run/docker.sock",
			expected:   RuntimeDocker,
		},
		{
			name:       "docker unix socket",
			socketPath: "unix:///var/run/docker.sock",
			expected:   RuntimeDocker,
		},
		{
			name:       "podman socket",
			socketPath: "/run/podman/podman.sock",
			expected:   RuntimePodman,
		},
		{
			name:       "rootless podman socket",
			socketPath: "/run/user/1000/podman/podman.sock",
			expected:   RuntimePodmanRootless,
		},
		{
			name:       "unknown socket defaults to docker",
			socketPath: "/unknown/socket.sock",
			expected:   RuntimeDocker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := detectRuntimeType(tt.socketPath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetDefaultSocketPaths(t *testing.T) {
	paths := getDefaultSocketPaths()
	
	// Should return common socket paths
	assert.True(t, len(paths) > 0)
	
	// Should contain at least one Docker socket path
	found := false
	for _, path := range paths {
		if strings.Contains(path, "docker") {
			found = true
			break
		}
	}
	assert.True(t, found, "Should contain a Docker socket path")
}

func TestGetDefaultPodmanSocket(t *testing.T) {
	socket := getDefaultPodmanSocket(false) // Regular mode
	
	// Should return a valid path
	assert.NotEmpty(t, socket)
	
	// Should contain "podman" in the path
	assert.Contains(t, socket, "podman")
	
	// Test rootless mode
	rootlessSocket := getDefaultPodmanSocket(true)
	assert.NotEmpty(t, rootlessSocket)
	assert.Contains(t, rootlessSocket, "podman")
}

func TestGetAvailableRuntimes(t *testing.T) {
	runtimes := GetAvailableRuntimes()
	
	// Should return a slice of runtime info
	assert.IsType(t, []RuntimeInfo{}, runtimes)
	
	// Should not be empty
	assert.True(t, len(runtimes) > 0)
	
	// Each runtime should have basic info
	for _, rt := range runtimes {
		assert.NotEmpty(t, rt.Type)
		assert.NotEmpty(t, rt.SocketPath)
		// Version may be empty if runtime not actually available
	}
}

// Test socket existence checking (private function behavior via public interface)
func TestSocketExistenceChecking(t *testing.T) {
	ctx := context.Background()
	
	// Create a temporary socket file
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "test.sock")
	
	// Create an empty file to simulate a socket
	file, err := os.Create(socketPath)
	require.NoError(t, err)
	file.Close()
	
	// Test with existing file (won't be a real socket, but tests file existence)
	_, err = createDockerRuntime(ctx, socketPath)
	// This will still fail because it's not a real Docker socket, but for a different reason
	assert.Error(t, err)
	// Error should not be about file not existing
	assert.NotContains(t, err.Error(), "no such file")
}