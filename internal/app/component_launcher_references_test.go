package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
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

func TestNewComponentLaunchPlanUsesCanonicalDomainNames(t *testing.T) {
	const migrationID = "fixture"
	const generation uint64 = 3
	plan, err := NewComponentLaunchPlan(MigrationCheckpoint{
		MigrationID:         migrationID,
		ComponentGeneration: generation,
		TargetVersion:       "v2",
		TargetImage:         "example.invalid/gordon:v2",
	})
	require.NoError(t, err)
	assert.Equal(t, domain.FormatComponentInternalNetwork(migrationID, generation), plan.InternalNetwork)
	for _, component := range plan.Components {
		assert.Equal(t, domain.FormatComponentID(component.Role, migrationID, generation), component.ComponentID)
	}

	updater := &recordingRuntimeSelfUpdater{}
	launcher, err := NewRuntimeComponentLauncher(updater)
	require.NoError(t, err)
	require.NoError(t, launcher.CreateInternalNetwork(context.Background(), plan))
	require.Len(t, updater.commands, 1)
	assert.Equal(t, domain.FormatComponentNetworkID(migrationID, generation), updater.commands[0].TargetComponentID)
}
