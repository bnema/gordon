package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitConfigRejectsLegacyPortsWithoutEntrypoints(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[server]\nport = 8088\ntls_port = 8443\n"), 0o600))

	_, _, err := initConfig(configPath)

	require.ErrorContains(t, err, "legacy server.port or server.tls_port configuration requires at least one [entrypoints] entry")
}

func TestInitConfigAcceptsLegacyPortsWithEntrypoint(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "gordon.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[server]\nport = 8088\n\n[entrypoints.edge]\naddress = \":443\"\nprotocol = \"smart_tcp\"\n"), 0o600))

	_, cfg, err := initConfig(configPath)

	require.NoError(t, err)
	require.Contains(t, cfg.EntryPoints, "edge")
}
