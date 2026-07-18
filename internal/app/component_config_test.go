package app

import (
	"os"
	"path/filepath"
	"strings"
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
	cfg.Auth.SecretsBackend = "unsafe"
	cfg.Runtime.Token = "private-runtime-handoff-token"
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
	control, err := os.ReadFile(byRole[domain.ComponentRoleControl])
	require.NoError(t, err)
	assert.Contains(t, string(control), "endpoint = 'unix:///var/lib/gordon/migration/fixture/runtime-control.sock'")
	assert.NotContains(t, string(control), "127.0.0.1:19444")
	runtime, err := os.ReadFile(byRole[domain.ComponentRoleRuntime])
	require.NoError(t, err)
	assert.Contains(t, string(runtime), "token_env = 'GORDON_COMPONENT_RUNTIME_TOKEN'")
	assert.Contains(t, string(runtime), "[auth]")
	assert.Contains(t, string(runtime), "secrets_backend = 'unsafe'", "runtime must initialize the component-token validator before binding its Unix socket")
	registry, err := os.ReadFile(byRole[domain.ComponentRoleRegistry])
	require.NoError(t, err)
	assert.NotContains(t, string(registry), "control-token-reference")
	assert.Contains(t, string(registry), "[storage]")
	assert.Contains(t, string(registry), "event_token_env")
	for _, file := range files {
		assert.Equal(t, ".toml", filepath.Ext(file.Path), "role configs must be consumable by role TOML decoders")
	}
}

func TestComponentConfigSeparatesPreparedProbeAndFinalPublicEdge(t *testing.T) {
	cfg := Config{}
	cfg.Server.Port, cfg.Server.RegistryPort = 18080, 15000
	cfg.Runtime.Token = "private-runtime-handoff-token"
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	preparedPath := componentConfigReferences(componentConfigPaths(files))[domain.ComponentRoleEdge]
	prepared, err := os.ReadFile(preparedPath)
	require.NoError(t, err)
	final, err := os.ReadFile(filepath.Join(filepath.Dir(preparedPath), "edge-final.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(prepared), "migration_probe_enabled = true")
	assert.Contains(t, string(prepared), "migration_probe_token_env = 'GORDON_MIGRATION_PROBE_TOKEN'")
	assert.NotContains(t, string(prepared), migrationProbeToken(cfg.Runtime.Token))
	assert.Contains(t, string(final), "migration_probe_enabled = false")
	assert.NotContains(t, string(final), "migration_probe_token_env")
	assert.NotContains(t, string(final), migrationProbeToken(cfg.Runtime.Token))
	finalConfig, err := initEdgeConfig(filepath.Join(filepath.Dir(preparedPath), "edge-final.toml"))
	require.NoError(t, err)
	assert.False(t, finalConfig.Edge.MigrationProbeEnabled)
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

func TestComponentConfigUsesPrivateControlBindAndInternalAlias(t *testing.T) {
	cfg := Config{}
	cfg.Control.ListenAddress = "127.0.0.1:9443"
	cfg.Control.Endpoint = "control.example.test:9443"
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	byRole := componentConfigReferences(componentConfigPaths(files))
	control, err := os.ReadFile(byRole[domain.ComponentRoleControl])
	require.NoError(t, err)
	assert.Contains(t, string(control), "listen_address = '0.0.0.0:9443'")
	assert.NotContains(t, string(control), cfg.Control.ListenAddress)
	for _, role := range []domain.ComponentRole{domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		contents, readErr := os.ReadFile(byRole[role])
		require.NoError(t, readErr)
		assert.Contains(t, string(contents), "gordon-control:9443")
		assert.NotContains(t, string(contents), cfg.Control.Endpoint)
	}
}

func TestWriteComponentConfigManifestsRejectsInvalidControlPort(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "127.0.0.1:not-a-port", "not-an-address"} {
		t.Run(address, func(t *testing.T) {
			cfg := Config{}
			cfg.Control.ListenAddress = address
			_, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
			require.Error(t, err)
			assert.ErrorContains(t, err, "control listen address")
		})
	}
}

func TestComponentConfigUsesInternalRuntimeAliasInsteadOfHostBootstrap(t *testing.T) {
	cfg := Config{}
	cfg.Runtime.Endpoint = "127.0.0.1:19444"
	files, err := WriteComponentConfigManifests(cfg, filepath.Join(t.TempDir(), "migration", "config", "fixture", "1"))
	require.NoError(t, err)
	control, err := os.ReadFile(componentConfigReferences(componentConfigPaths(files))[domain.ComponentRoleControl])
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(control), "endpoint = 'unix:///var/lib/gordon/migration/fixture/runtime-control.sock'"))
	assert.NotContains(t, string(control), cfg.Runtime.Endpoint)
}

func componentConfigPaths(files []ComponentConfigFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}
