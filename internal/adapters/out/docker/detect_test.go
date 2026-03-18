package docker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectRuntimeSocket_ExplicitPathTakesPrecedence(t *testing.T) {
	result := DetectRuntimeSocket("/custom/sock")
	assert.Equal(t, "/custom/sock", result.SocketPath)
	assert.Equal(t, "explicit", result.Source)
}

func TestDetectRuntimeSocket_FallsBackToDockerHost(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///tmp/test-docker.sock")
	result := DetectRuntimeSocket("")
	assert.Equal(t, "/tmp/test-docker.sock", result.SocketPath)
	assert.Equal(t, "DOCKER_HOST", result.Source)
}

func TestDetectRuntimeSocket_EmptyDockerHostIgnored(t *testing.T) {
	t.Setenv("DOCKER_HOST", "")
	result := DetectRuntimeSocket("")
	assert.NotEqual(t, "DOCKER_HOST", result.Source)
}

func TestDetectRuntimeSocket_NonUnixDockerHostPassedThrough(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://localhost:2375")
	result := DetectRuntimeSocket("")
	assert.Equal(t, "", result.SocketPath)
	assert.Equal(t, "DOCKER_HOST_passthrough", result.Source)
}

func TestParseUnixSocketPath(t *testing.T) {
	tests := []struct {
		input string
		path  string
		ok    bool
	}{
		{"unix:///var/run/docker.sock", "/var/run/docker.sock", true},
		{"unix:///run/podman/podman.sock", "/run/podman/podman.sock", true},
		{"tcp://localhost:2375", "", false},
		{"/var/run/docker.sock", "/var/run/docker.sock", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			path, ok := parseUnixSocketPath(tt.input)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.path, path)
			}
		})
	}
}

func TestNewRuntimeWithSocket_UsesExplicitHost(t *testing.T) {
	rt, err := NewRuntimeWithSocket("/nonexistent/test.sock")
	require.NoError(t, err)
	require.NotNil(t, rt)
	assert.Contains(t, rt.client.DaemonHost(), "/nonexistent/test.sock")
}
