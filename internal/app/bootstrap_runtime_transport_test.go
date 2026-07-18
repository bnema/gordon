package app

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestMigrationBootstrapRuntimeTransportUsesPrivateUnixSocketWithoutPublish(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared}
	service := &MigrationService{}
	require.NoError(t, service.setBootstrapListeners(&checkpoint))

	assert.Equal(t, "unix:///var/lib/gordon/migration/fixture/runtime-control.sock", checkpoint.BootstrapRuntimeEndpoint)
	assert.Empty(t, checkpoint.PreparedPortBindings)
	require.NoError(t, validateCheckpoint(checkpoint))
}

func TestMigrationBootstrapRuntimeTransportRejectsTraversalTCPAndOtherSockets(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared}
	for _, endpoint := range []string{
		"127.0.0.1:9444", "unix:///var/lib/gordon/migration/fixture/../runtime-control.sock",
		"unix:///var/lib/gordon/migration/fixture/podman.sock", "unix:///tmp/runtime-control.sock",
		"unix://host/var/lib/gordon/migration/fixture/runtime-control.sock",
	} {
		checkpoint.BootstrapRuntimeEndpoint = endpoint
		assert.Error(t, validateCheckpoint(checkpoint), endpoint)
	}
}

func TestComponentLaunchPlanRejectsTCPBootstrapAndPublish(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, BootstrapRuntimeEndpoint: "127.0.0.1:23456"}
	_, err := NewComponentLaunchPlan(checkpoint)
	require.Error(t, err)

	checkpoint.BootstrapRuntimeEndpoint = "unix:///var/lib/gordon/migration/fixture/runtime-control.sock"
	checkpoint.PreparedPortBindings = []MigrationPortBinding{{Role: "runtime", HostIP: "127.0.0.1", HostPort: 23456, ContainerPort: 9444, Protocol: "tcp"}}
	_, err = NewComponentLaunchPlan(checkpoint)
	require.Error(t, err)
}

func TestRuntimeHandoffDialerRejectsWrongRoleTokenAndNonUnixEndpoint(t *testing.T) {
	dial := newRuntimeHandoffDialer(RuntimeControlConfig{Token: "fixture-token"})
	component := ComponentLaunchComponent{Role: domain.ComponentRoleRuntime, BootstrapEndpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock"}
	_, err := dial(t.Context(), component)
	require.NoError(t, err, "dial construction is lazy and accepts only the private Unix endpoint")

	component.Role = domain.ComponentRoleEdge
	_, err = dial(t.Context(), component)
	require.Error(t, err)
	component.Role = domain.ComponentRoleRuntime
	component.BootstrapEndpoint = "host.containers.internal:23456"
	_, err = dial(t.Context(), component)
	require.Error(t, err)

	missingToken := newRuntimeHandoffDialer(RuntimeControlConfig{})
	_, err = missingToken(t.Context(), ComponentLaunchComponent{Role: domain.ComponentRoleRuntime, BootstrapEndpoint: "unix:///var/lib/gordon/migration/fixture/runtime-control.sock"})
	require.Error(t, err)
}

func TestRuntimeBootstrapSocketPathUsesConfiguredMigrationDirectory(t *testing.T) {
	root := t.TempDir()
	endpoint := "unix://" + filepath.Join(root, "migration", "fixture", bootstrapRuntimeSocketName)
	path, ok := runtimeBootstrapSocketPath(endpoint, root)
	require.True(t, ok)
	assert.Equal(t, filepath.Join(root, "migration", "fixture", bootstrapRuntimeSocketName), path)
}
