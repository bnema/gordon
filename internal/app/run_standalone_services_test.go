package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInitConfigAcceptsStandaloneServices proves the installation-level
// [[services]] L4 standalone workload key is still live config and must
// not be rejected as a retired app key. App workloads now live in
// standalone app files, but [[services]] in gordon.toml keeps managing
// Gordon-owned L4 containers.
func TestInitConfigAcceptsStandaloneServices(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	doc := `
[[services]]
name = "rust"
image = "registry.example.com:5000/rust:latest"
enabled = true

[[services.ports]]
name = "game"
container = 28015
protocol = "udp"
publish = "127.0.0.1:38015"
`
	require.NoError(t, os.WriteFile(configPath, []byte(doc), 0o600))

	_, cfg, err := initConfig(configPath)

	require.NoError(t, err)
	require.Len(t, cfg.Services, 1)
	require.Equal(t, "rust", cfg.Services[0].Name)
	require.True(t, cfg.Services[0].Enabled)
	require.Len(t, cfg.Services[0].Ports, 1)
	require.Equal(t, 28015, cfg.Services[0].Ports[0].Container)
}
