package container

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeComponentLifecycleUsesExactRootlessIdentityForEveryRole(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: "gordon-control-secrets-0123456789abcdef"}}

	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry, domain.ComponentRoleEdge} {
		t.Run(string(role), func(t *testing.T) {
			configPath := filepath.Join(configDir, string(role)+".toml")
			require.NoError(t, os.WriteFile(configPath, []byte("[component]\n"), 0o600))
			command := managedSecretsLifecycleCommand(role, domain.RuntimeComponentLifecycleStart, configPath)
			config, err := manager.componentConfig(command, nil)
			require.NoError(t, err)
			identity, ok := domain.FixedComponentProcessIdentity(role)
			require.True(t, ok)
			assert.Equal(t, identity.User, config.User)
			assert.Equal(t, "keep-id:uid="+strconv.Itoa(identity.UID)+",gid="+strconv.Itoa(identity.GID), config.UsernsMode)
			assert.Empty(t, config.GroupAdd)
			assert.Equal(t, []string{"ALL"}, config.CapDrop)
			assert.Empty(t, config.CapAdd)
			require.NotNil(t, config.NoNewPrivileges)
			assert.True(t, *config.NoNewPrivileges)
		})
	}
}

func TestRuntimeComponentLifecycleRejectsForgedExistingIdentity(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "edge.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[edge]\n"), 0o600))
	command := managedSecretsLifecycleCommand(domain.ComponentRoleEdge, domain.RuntimeComponentLifecycleHealth, configPath)
	identity, _ := domain.FixedComponentProcessIdentity(domain.ComponentRoleEdge)
	valid := domain.Container{
		ID: "existing", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command), User: identity.User,
		UsernsMode: "keep-id:uid=21003,gid=21003", CapDrop: []string{"ALL"}, NoNewPrivileges: true,
		VolumeMounts: []domain.ContainerVolumeMount{{Type: "bind", Source: configPath, Destination: "/etc/gordon/role.toml", ReadOnly: true}},
	}
	for name, forge := range map[string]func(*domain.Container){
		"root user":                 func(c *domain.Container) { c.User = "0:0" },
		"generic user":              func(c *domain.Container) { c.User = "gordon" },
		"wrong user namespace":      func(c *domain.Container) { c.UsernsMode = "keep-id" },
		"supplemental group":        func(c *domain.Container) { c.GroupAdd = []string{"21001"} },
		"missing capability drop":   func(c *domain.Container) { c.CapDrop = nil },
		"added capability":          func(c *domain.Container) { c.CapAdd = []string{"NET_BIND_SERVICE"} },
		"missing no-new-privileges": func(c *domain.Container) { c.NoNewPrivileges = false },
		"missing managed mount":     func(c *domain.Container) { c.VolumeMounts = nil },
		"writable config":           func(c *domain.Container) { c.VolumeMounts[0].ReadOnly = false },
		"duplicate config":          func(c *domain.Container) { c.VolumeMounts = append(c.VolumeMounts, c.VolumeMounts[0]) },
		"foreign config":            func(c *domain.Container) { c.VolumeMounts[0].Source = filepath.Join(configDir, "foreign.toml") },
	} {
		t.Run(name, func(t *testing.T) {
			container := valid
			container.GroupAdd = append([]string(nil), valid.GroupAdd...)
			container.CapDrop = append([]string(nil), valid.CapDrop...)
			container.CapAdd = append([]string(nil), valid.CapAdd...)
			container.VolumeMounts = append([]domain.ContainerVolumeMount(nil), valid.VolumeMounts...)
			forge(&container)
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{&container}, nil).Once()
			runtime.EXPECT().InspectContainer(mock.Anything, container.ID).Return(&container, nil).Once()
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
			require.ErrorIs(t, manager.ApplyComponentLifecycle(t.Context(), command), ErrRuntimePolicyDenied)
		})
	}
}

func TestRuntimeComponentLifecycleRejectsUnlabeledGordonNamedWorkload(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	workload := &domain.Container{ID: "workload", Name: "gordon-edge-fixture-g1", Labels: map[string]string{domain.LabelDomain: "app.example"}}
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{workload}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, workload.ID).Return(workload, nil).Once()
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
	configPath := filepath.Join(configDir, "edge.toml")
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

func TestRuntimeComponentLifecycleRejectsAnotherRolesGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "migration", "config", "fixture", "2")
	envDir := filepath.Join(root, "migration", "env", "fixture", "2")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.MkdirAll(envDir, 0o700))
	controlConfig := filepath.Join(configDir, "control.toml")
	controlEnv := filepath.Join(envDir, "control.env")
	require.NoError(t, os.WriteFile(controlConfig, []byte("[control]\n"), 0o600))
	require.NoError(t, os.WriteFile(controlEnv, []byte("SAFE=value\n"), 0o600))
	manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce}}
	command := managedSecretsLifecycleCommand(domain.ComponentRoleEdge, domain.RuntimeComponentLifecycleStart, controlConfig)
	command.Generation = 2
	command.TargetComponentID = "gordon-edge-fixture-g2"
	command.EnvironmentFile = controlEnv
	_, err := manager.componentConfig(command, nil)
	require.Error(t, err)
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
