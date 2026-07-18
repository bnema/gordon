package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestWriteComponentConfigManifestsScopesRolesAndPermissions(t *testing.T) {
	cfg := Config{}
	cfg.Server.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Server.Port = 8080
	cfg.Server.RegistryPort = 5000
	cfg.Auth.TokenSecret = "control-token-reference"
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	require.Len(t, files, 4)
	byRole := componentConfigReferences(componentConfigPaths(files))
	require.Contains(t, byRole, domain.ComponentRoleControl)
	require.Contains(t, byRole, domain.ComponentRoleRuntime)
	require.Contains(t, byRole, domain.ComponentRoleRegistry)
	require.Contains(t, byRole, domain.ComponentRoleEdge)
	for _, file := range files {
		info, statErr := os.Stat(file.Path)
		require.NoError(t, statErr)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	edge, err := os.ReadFile(byRole[domain.ComponentRoleEdge])
	require.NoError(t, err)
	assert.NotContains(t, string(edge), "control-token-reference")
	assert.Contains(t, string(edge), "[edge]")
	assert.Contains(t, string(edge), "token_env")
	registry, err := os.ReadFile(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err)
	assert.NotContains(t, string(registry), "control-token-reference")
	assert.Contains(t, string(registry), "[storage]")
	assert.Contains(t, string(registry), "event_token_env")
	for _, file := range files {
		assert.Equal(t, ".toml", filepath.Ext(file.Path), "role configs must be consumable by role TOML decoders")
	}
}

func TestComponentConfigManifestsUseStrictRoleSchemas(t *testing.T) {
	cfg := Config{}
	cfg.Server.DataDir = t.TempDir()
	cfg.Server.Port = 18080
	cfg.Server.RegistryPort = 15000
	cfg.Server.MaxBlobChunkSize = "95MB"
	cfg.Server.MaxBlobSize = "1GB"
	cfg.Control.Endpoint = "gordon-control:9443"
	cfg.Control.InsecureTLS = true
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	byRole := componentConfigReferences(componentConfigPaths(files))
	_, err = initEdgeConfig(byRole[domain.ComponentRoleEdge])
	require.NoError(t, err, "edge config must be accepted by its strict decoder")
	_, err = initRegistryConfig(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err, "registry config must be accepted by its strict decoder")
}

func componentConfigPaths(files []ComponentConfigFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}
