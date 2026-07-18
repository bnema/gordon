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
	err := manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "port", IdempotencyKey: "port", Generation: 1, SourceComponentID: "gordon-control"},
		TargetComponentID:      "gordon-runtime-fixture-g1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart,
		DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: "/not/used", PreserveVolumes: true,
		PortPublishes: []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 2375, ContainerPort: 2375, Protocol: domain.NetworkProtocolTCP}},
	})
	require.Error(t, err)
}
