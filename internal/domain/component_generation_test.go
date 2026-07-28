package domain

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDomainComponentGenerationNameRoundTrip(t *testing.T) {
	migrationID := "fixture"
	generation := uint64(7)
	for _, role := range []ComponentRole{ComponentRoleControl, ComponentRoleRuntime, ComponentRoleRegistry, ComponentRoleEdge} {
		componentID := FormatComponentID(role, migrationID, generation)
		volumeName := FormatComponentGenerationVolumeName(role, migrationID, generation)
		assert.Equal(t, componentID, volumeName)
		assert.True(t, MatchComponentLifecycleTarget(RuntimeComponentLifecycleStart, componentID, role, migrationID, generation))
		assert.False(t, MatchComponentLifecycleTarget(RuntimeComponentLifecycleStart, componentID+"x", role, migrationID, generation))

		inspected := &Container{
			Name: componentID,
			Labels: map[string]string{
				LabelComponentRole:        string(role),
				LabelComponentMigrationID: migrationID,
				LabelComponentGeneration:  "7",
			},
		}
		if role == ComponentRoleEdge {
			_, ok := MatchComponentGenerationVolume(inspected)
			assert.False(t, ok)
			continue
		}
		matched, ok := MatchComponentGenerationVolume(inspected)
		require.True(t, ok)
		assert.Equal(t, volumeName, matched)
	}

	networkID := FormatComponentNetworkID(migrationID, generation)
	assert.True(t, MatchComponentLifecycleTarget(RuntimeComponentLifecycleEnsureNetwork, networkID, ComponentRoleRuntime, migrationID, generation))
	assert.Equal(t, "gordon-internal-fixture-g7", FormatComponentInternalNetwork(migrationID, generation))
}

func TestApprovedGeneratedRolePathMatchesMigrationLayout(t *testing.T) {
	root := filepath.Join("/var/lib/gordon", "migration")
	path := filepath.Join(root, "config", "fixture", "7", "control.toml")
	assert.True(t, ApprovedGeneratedRolePath(path, root, "config", "fixture", 7, "control.toml"))
	assert.False(t, ApprovedGeneratedRolePath(path, root, "env", "fixture", 7, "control.toml"))
}

func TestValidManagedControlSecretsVolumeCentralized(t *testing.T) {
	assert.True(t, ValidManagedControlSecretsVolume("gordon-control-secrets-0123456789abcdef"))
	assert.False(t, ValidManagedControlSecretsVolume("gordon-control-secrets-invalid"))
	assert.False(t, ValidManagedControlSecretsVolume("gordon-control-fixture-g1"))
}
