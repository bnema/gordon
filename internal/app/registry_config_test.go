package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeRegistryConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.toml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0600))
	return path
}
func TestRegistryConfigRejectsFullRoleKeys(t *testing.T) {
	path := writeRegistryConfig(t, `[storage]
data_dir = "/tmp/data"
[listen]
address = "127.0.0.1:5000"
[listen.tls]
mode = "disabled"
[control]
event_endpoint = "127.0.0.1:9092"
event_token = "token"
[server]
runtime = "unix:///var/run/docker.sock"
`)
	_, err := initRegistryConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "strict mode")
}
func TestRegistryConfigOnlyAllowsExplicitTLSModes(t *testing.T) {
	path := writeRegistryConfig(t, `[storage]
data_dir = "/tmp/data"
[listen]
address = "127.0.0.1:5000"
[listen.tls]
mode = "external"
[control]
event_endpoint = "127.0.0.1:9092"
event_token = "token"
`)
	_, err := initRegistryConfig(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "listen.tls.mode")
}
