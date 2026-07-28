package app

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateComponentLaunchReferencesRequiresEnvPerRoleWhenPresent(t *testing.T) {
	root := filepath.Join("/var/lib/gordon", "migration")
	checkpoint := MigrationCheckpoint{
		MigrationID:         "fixture",
		ComponentGeneration: 1,
		ConfigFileReferences: []string{
			filepath.Join(root, "config", "fixture", "1", "control.toml"),
			filepath.Join(root, "config", "fixture", "1", "runtime.toml"),
			filepath.Join(root, "config", "fixture", "1", "registry.toml"),
			filepath.Join(root, "config", "fixture", "1", "edge.toml"),
		},
		EnvFileReferences: []string{
			filepath.Join(root, "env", "fixture", "1", "control.env"),
			filepath.Join(root, "env", "fixture", "1", "runtime.env"),
		},
	}
	configByRole := componentConfigReferences(checkpoint.ConfigFileReferences)
	envByRole := componentEnvReferences(checkpoint.EnvFileReferences)
	require.Error(t, validateComponentLaunchReferences(checkpoint, envByRole, configByRole))

	checkpoint.EnvFileReferences = append(checkpoint.EnvFileReferences,
		filepath.Join(root, "env", "fixture", "1", "registry.env"),
		filepath.Join(root, "env", "fixture", "1", "edge.env"),
	)
	envByRole = componentEnvReferences(checkpoint.EnvFileReferences)
	require.NoError(t, validateComponentLaunchReferences(checkpoint, envByRole, configByRole))
}
