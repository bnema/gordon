package container

import (
	"context"
	"os"
	"path/filepath"
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

func TestRuntimeComponentLifecycleStartUsesRoleConfigAndPersistentStorage(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "2")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "edge.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("server: {}\n"), 0o600))
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return(nil, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(config *domain.ContainerConfig) bool {
		return config.Name == "gordon-edge-fixture-g2" && config.Labels[domain.LabelComponent] == "true" && config.Labels[domain.LabelComponentRole] == "edge" && config.Labels[domain.LabelComponentGeneration] == "2" && config.Labels[domain.LabelComponentMigrationID] == "fixture" && len(config.Volumes) == 0 && config.ReadOnlyVolumes["/etc/gordon/role.toml"] == configPath && len(config.Cmd) == 5 && config.Cmd[0] == "serve" && config.Cmd[2] == "edge"
	})).Return(&domain.Container{ID: "edge"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "edge").Return(nil).Once()
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	err := manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "test", IdempotencyKey: "test", Generation: 2, SourceComponentID: "gordon-control"}, TargetComponentID: "gordon-edge-fixture-g2", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g2", ConfigFile: configPath, PortPublishes: []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8081, Protocol: domain.NetworkProtocolTCP}}, PreserveVolumes: true,
	})
	require.NoError(t, err)
}

func TestRuntimeComponentSocketMountIsRuntimeOnly(t *testing.T) {
	source, env := runtimeComponentSocketMount([]string{"PODMAN_HOST=unix:///run/user/1000/podman/podman.sock", "SAFE=value"})
	require.Equal(t, "/run/user/1000/podman/podman.sock", source)
	require.Contains(t, env, "PODMAN_HOST=unix:///run/gordon/runtime.sock")
	source, _ = runtimeComponentSocketMount([]string{"SAFE=value"})
	require.Empty(t, source)
}

func TestRuntimeComponentLifecycleStartRejectsMissingRoleConfig(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	err := manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "test", IdempotencyKey: "test", Generation: 2, SourceComponentID: "gordon-control"}, TargetComponentID: "gordon-edge-fixture-g2", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g2", PreserveVolumes: true,
	})
	require.Error(t, err)
}
