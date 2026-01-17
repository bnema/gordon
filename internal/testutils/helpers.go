package testutils

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// TestContext creates a test context with timeout
func TestContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// CreateTempConfig creates a temporary configuration file
func CreateTempConfig(t *testing.T, content string) afero.Fs {
	fs := afero.NewMemMapFs()
	err := afero.WriteFile(fs, "/config.toml", []byte(content), 0644)
	require.NoError(t, err)
	return fs
}

// AssertEventuallyTrue retries a condition until it's true or times out
func AssertEventuallyTrue(t *testing.T, condition func() bool, timeout time.Duration, message string) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Condition never became true: %s", message)
}

// LoadFixtureConfig loads a configuration fixture file
func LoadFixtureConfig(t *testing.T, filename string) string {
	content := `[server]
port = 8080
registry_port = 5000
registry_domain = "registry.example.com"
runtime = "docker"

[auth]
enabled = true
secrets_backend = "unsafe"
username = "admin"
password = "password123"

[routes]
"app.example.com" = "nginx:latest"
"api.example.com" = "myapi:v1"

[volumes]
auto_create = true
prefix = "gordon"
preserve = true

[env]
dir = "/tmp/env"
providers = ["pass", "sops"]

[logging]
enabled = true
level = "info"`

	switch filename {
	case "minimal.toml":
		return `[server]
registry_domain = "registry.example.com"
runtime = "docker"

[auth]
enabled = false`
	case "invalid.toml":
		return `[server
port = 8080`
	default:
		return content
	}
}
