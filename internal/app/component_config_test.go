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
	registry, err := os.ReadFile(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err)
	assert.NotContains(t, string(registry), "control-token-reference")
}

func componentConfigPaths(files []ComponentConfigFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}
