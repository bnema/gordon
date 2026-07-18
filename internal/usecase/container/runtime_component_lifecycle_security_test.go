package container

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeComponentLifecycleRejectsUnlabeledGordonNamedWorkload(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{{ID: "workload", Name: "gordon-edge-fixture-g1", Labels: map[string]string{domain.LabelDomain: "app.example"}}}, nil).Once()
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	err := manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("component-security"), TargetComponentID: "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStop, PreserveVolumes: true,
	})
	require.Error(t, err)
}

func TestRuntimeComponentLifecycleStartCreatesFullyLabeledSocketlessComponent(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return(nil, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(config *domain.ContainerConfig) bool {
		return config.Name == "gordon-edge-fixture-g2" && config.Labels[domain.LabelComponent] == "true" && config.Labels[domain.LabelComponentRole] == "edge" && config.Labels[domain.LabelComponentGeneration] == "2" && config.Labels[domain.LabelComponentMigrationID] == "fixture" && len(config.Volumes) == 0 && len(config.ReadOnlyVolumes) == 0
	})).Return(&domain.Container{ID: "edge"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "edge").Return(nil).Once()
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	err := manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "test", IdempotencyKey: "test", Generation: 2, SourceComponentID: "gordon-control"}, TargetComponentID: "gordon-edge-fixture-g2", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g2", PreserveVolumes: true,
	})
	require.NoError(t, err)
}
