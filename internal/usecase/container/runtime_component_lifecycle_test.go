package container

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeComponentLifecycleUsesRuntimeOnlyContainerProtocol(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "control.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[server]\n"), 0o600))
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
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-control-fixture-g1", TargetComponentRole: domain.ComponentRoleControl, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture-hash", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: configPath, PreserveVolumes: true}))
}

func TestComponentLifecycleMountsOnlyPrivateMigrationSocketStateForRuntimeAndControl(t *testing.T) {
	data := t.TempDir()
	configDir := filepath.Join(data, "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "runtime.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[runtime]\n"), 0o600))
	manager := NewRuntimeComponentLifecycleManager(outmocks.NewMockContainerRuntime(t), RuntimePolicy{Mode: RuntimePolicyModeEnforce, MigrationStateRoot: filepath.Join(data, "migration")}).(*runtimeComponentLifecycleManager)
	identity := testRuntimeCommandIdentity("component-socket-state")
	command := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-runtime-fixture-g1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture-hash", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: configPath, PreserveVolumes: true}
	config, err := manager.componentConfig(command, nil)
	require.NoError(t, err)
	state := filepath.Join(data, "migration", "fixture")
	stateInfo, statErr := os.Stat(state)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o700), stateInfo.Mode().Perm())
	assert.Equal(t, state, config.Volumes["/var/lib/gordon/migration/fixture"])
	assert.NotContains(t, config.ReadOnlyVolumes, "/var/lib/gordon/migration/fixture")
	assert.NotContains(t, config.Volumes, "/run/gordon/runtime.sock")

	command.TargetComponentRole = domain.ComponentRoleControl
	command.TargetComponentID = "gordon-control-fixture-g1"
	config, err = manager.componentConfig(command, nil)
	require.NoError(t, err)
	assert.Equal(t, state, config.ReadOnlyVolumes["/var/lib/gordon/migration/fixture"])
	assert.NotContains(t, config.Volumes, "/var/lib/gordon/migration/fixture")

	command.TargetComponentRole = domain.ComponentRoleEdge
	command.TargetComponentID = "gordon-edge-fixture-g1"
	config, err = manager.componentConfig(command, nil)
	require.NoError(t, err)
	assert.NotContains(t, config.Volumes, "/var/lib/gordon/migration/fixture")
	assert.NotContains(t, config.ReadOnlyVolumes, "/var/lib/gordon/migration/fixture")
}
