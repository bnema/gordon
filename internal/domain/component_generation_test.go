package domain

import (
	"path/filepath"
	"strings"
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
	assert.True(t, ApprovedGeneratedRolePath(path, "", "config", "fixture", 7, "control.toml"))
	assert.False(t, ApprovedGeneratedRolePath(path, root, "env", "fixture", 7, "control.toml"))
	assert.False(t, ApprovedGeneratedRolePath("migration/config/fixture/7/control.toml", root, "config", "fixture", 7, "control.toml"))
	assert.False(t, ApprovedGeneratedRolePath(root+"/config/fixture/7/../7/control.toml", root, "config", "fixture", 7, "control.toml"))
}

func TestMatchComponentGenerationVolumeRejectsMalformedLabels(t *testing.T) {
	for _, tc := range []struct{ generation, migrationID string }{
		{"x", "fixture"}, {"0", "fixture"}, {"07", "fixture"}, {"7", " fixture "},
	} {
		inspected := &Container{
			Name: FormatComponentID(ComponentRoleControl, "fixture", 7),
			Labels: map[string]string{
				LabelComponentRole:        string(ComponentRoleControl),
				LabelComponentMigrationID: tc.migrationID,
				LabelComponentGeneration:  tc.generation,
			},
		}
		_, ok := MatchComponentGenerationVolume(inspected)
		assert.False(t, ok, "generation %q migration ID %q", tc.generation, tc.migrationID)
	}
}

func TestValidComponentMigrationIDRejectsUnsafeInput(t *testing.T) {
	for _, id := range []string{"Fixture", "fix_ture", strings.Repeat("a", 129)} {
		assert.False(t, ValidComponentMigrationID(id), "id %q", id)
	}
}

func TestValidManagedControlSecretsVolumeCentralized(t *testing.T) {
	assert.True(t, ValidManagedControlSecretsVolume("gordon-control-secrets-0123456789abcdef"))
	assert.False(t, ValidManagedControlSecretsVolume("gordon-control-secrets-invalid"))
	assert.False(t, ValidManagedControlSecretsVolume("gordon-control-fixture-g1"))
}
