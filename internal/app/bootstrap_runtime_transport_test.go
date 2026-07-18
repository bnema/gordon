package app

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestMigrationBootstrapRuntimeTransportUsesPrivateGatewayAndRandomLoopbackPort(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared}
	service := &MigrationService{}
	require.NoError(t, service.setBootstrapListeners(&checkpoint))

	assert.True(t, strings.HasPrefix(checkpoint.BootstrapRuntimeEndpoint, "host.containers.internal:"))
	assert.NotContains(t, checkpoint.BootstrapRuntimeEndpoint, "127.0.0.1")
	require.Len(t, checkpoint.PreparedPortBindings, 1)
	binding := checkpoint.PreparedPortBindings[0]
	assert.Equal(t, "runtime", binding.Role)
	assert.Equal(t, "127.0.0.1", binding.HostIP)
	assert.Equal(t, 9444, binding.ContainerPort)
	assert.GreaterOrEqual(t, binding.HostPort, bootstrapRuntimePortMin)
	assert.LessOrEqual(t, binding.HostPort, bootstrapRuntimePortMax)
	require.NoError(t, validateCheckpoint(checkpoint))
}

func TestMigrationBootstrapRuntimeTransportRejectsUnreachableOrArbitraryEndpoints(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, BootstrapRuntimeEndpoint: "host.containers.internal:23456", PreparedPortBindings: []MigrationPortBinding{{Role: "runtime", HostIP: "127.0.0.1", HostPort: 23456, ContainerPort: 9444, Protocol: "tcp"}}}
	require.NoError(t, validateCheckpoint(checkpoint))

	for _, endpoint := range []string{"127.0.0.1:23456", "localhost:23456", "host.containers.internal:2375", "attacker.example:23456"} {
		checkpoint.BootstrapRuntimeEndpoint = endpoint
		assert.Error(t, validateCheckpoint(checkpoint), endpoint)
	}
	checkpoint.BootstrapRuntimeEndpoint = "host.containers.internal:23456"
	checkpoint.PreparedPortBindings[0].HostPort = 23457
	assert.Error(t, validateCheckpoint(checkpoint), "endpoint port must match the runtime-owned publish")
}

func TestComponentLaunchPlanRejectsNonGatewayRuntimeBootstrap(t *testing.T) {
	checkpoint := MigrationCheckpoint{MigrationID: "fixture", TargetImage: "example.invalid/gordon:fixture", ComponentGeneration: 1, StartedAt: time.Now().UTC(), Phase: MigrationPhasePrepared, BootstrapRuntimeEndpoint: "127.0.0.1:23456", PreparedPortBindings: []MigrationPortBinding{{Role: "runtime", HostIP: "127.0.0.1", HostPort: 23456, ContainerPort: 9444, Protocol: "tcp"}}}
	_, err := NewComponentLaunchPlan(checkpoint)
	require.Error(t, err)
}

func TestRuntimeHandoffDialerRejectsContainerLoopbackAndArbitraryGateway(t *testing.T) {
	dial := newRuntimeHandoffDialer(RuntimeControlConfig{Token: "fixture-token"})
	component := ComponentLaunchComponent{PortPublishes: componentPreparedPorts([]MigrationPortBinding{{Role: "runtime", HostIP: "127.0.0.1", HostPort: 23456, ContainerPort: 9444, Protocol: "tcp"}}, domain.ComponentRoleRuntime)}
	for _, endpoint := range []string{"127.0.0.1:23456", "gateway.invalid:23456", "host.containers.internal:2375"} {
		component.BootstrapEndpoint = endpoint
		_, err := dial(t.Context(), component)
		require.Error(t, err, endpoint)
	}
}
