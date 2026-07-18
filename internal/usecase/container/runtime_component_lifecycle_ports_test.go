package container

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeComponentLifecycleRejectsArbitraryPreparedPortBinding(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	for _, test := range []struct {
		name string
		role domain.ComponentRole
		port domain.ContainerPortPublish
	}{
		{name: "public runtime", role: domain.ComponentRoleRuntime, port: domain.ContainerPortPublish{HostIP: "0.0.0.0", HostPort: 2375, ContainerPort: 2375, Protocol: domain.NetworkProtocolTCP}},
		{name: "fixed legacy runtime", role: domain.ComponentRoleRuntime, port: domain.ContainerPortPublish{HostIP: "127.0.0.1", HostPort: 19444, ContainerPort: 9444, Protocol: domain.NetworkProtocolTCP}},
		{name: "edge bootstrap", role: domain.ComponentRoleEdge, port: domain.ContainerPortPublish{HostIP: "127.0.0.1", HostPort: 28080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}},
		{name: "registry bootstrap", role: domain.ComponentRoleRegistry, port: domain.ContainerPortPublish{HostIP: "127.0.0.1", HostPort: 25000, ContainerPort: 5000, Protocol: domain.NetworkProtocolTCP}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{
				RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "port", IdempotencyKey: "port", Generation: 1, SourceComponentID: "gordon-control"},
				TargetComponentID:      "gordon-" + string(test.role) + "-fixture-g1", TargetComponentRole: test.role, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart,
				DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: "/not/used", PreserveVolumes: true,
				PortPublishes: []domain.ContainerPortPublish{test.port},
			})
			require.Error(t, err)
		})
	}
}
