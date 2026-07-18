package container

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeComponentLifecycleUsesRuntimeOnlyContainerProtocol(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().NetworkExists(mock.Anything, "gordon-internal-fixture-g1").Return(false, nil).Once()
	runtime.EXPECT().CreateNetwork(mock.Anything, "gordon-internal-fixture-g1", mock.MatchedBy(func(config domain.NetworkConfig) bool { return config.Internal && config.Driver == "bridge" })).Return(nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return(nil, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(config *domain.ContainerConfig) bool {
		return config.Name == "gordon-control-fixture-g1" && config.NetworkMode == "gordon-internal-fixture-g1" && config.Labels[domain.LabelComponent] == "true"
	})).Return(&domain.Container{ID: "component-id"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "component-id").Return(nil).Once()

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	identity := testRuntimeCommandIdentity("component-lifecycle")
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-network-fixture-g1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleEnsureNetwork, InternalNetwork: "gordon-internal-fixture-g1", PreserveVolumes: true}))
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-control-fixture-g1", TargetComponentRole: domain.ComponentRoleControl, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture-hash", InternalNetwork: "gordon-internal-fixture-g1", PreserveVolumes: true}))
}
