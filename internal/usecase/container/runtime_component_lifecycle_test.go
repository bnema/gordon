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

func TestApprovedPreparedPortPublishesPermitsOnlyOnePrivateEdgeProbe(t *testing.T) {
	valid := []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8081, Protocol: domain.NetworkProtocolTCP}}
	assert.True(t, approvedPreparedPortPublishes(domain.ComponentRoleEdge, valid))

	for _, test := range []struct {
		role  domain.ComponentRole
		ports []domain.ContainerPortPublish
	}{
		{domain.ComponentRoleRuntime, valid},
		{domain.ComponentRoleEdge, nil},
		{domain.ComponentRoleEdge, append(valid, valid[0])},
		{domain.ComponentRoleEdge, []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 18080, ContainerPort: 8081, Protocol: domain.NetworkProtocolTCP}}},
		{domain.ComponentRoleEdge, []domain.ContainerPortPublish{{HostIP: "192.0.2.1", HostPort: 18080, ContainerPort: 8081, Protocol: domain.NetworkProtocolTCP}}},
		{domain.ComponentRoleEdge, []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8081, Protocol: domain.NetworkProtocol("udp")}}},
		{domain.ComponentRoleEdge, []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 8081, ContainerPort: 8081, Protocol: domain.NetworkProtocolTCP}}},
	} {
		assert.False(t, approvedPreparedPortPublishes(test.role, test.ports), "%+v", test)
	}
}

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

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: "gordon-control-secrets-0123456789abcdef"})
	identity := testRuntimeCommandIdentity("component-lifecycle")
	identity.SourceComponentID = "gordon-control"
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-network-fixture-g1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleEnsureNetwork, InternalNetwork: "gordon-internal-fixture-g1", PreserveVolumes: true}))
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-control-fixture-g1", TargetComponentRole: domain.ComponentRoleControl, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture-hash", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: configPath, PreserveVolumes: true}))
}

func TestRuntimeComponentLifecycleConnectsPreparedEdgeOnlyToValidatedManagedAppNetwork(t *testing.T) {
	identity := testRuntimeCommandIdentity("component-connect")
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: identity, TargetComponentID: "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: domain.RuntimeComponentLifecycleConnect, InternalNetwork: "gordon-app-fixture", PreserveVolumes: true,
	}
	edge := &domain.Container{ID: "edge-id", Name: command.TargetComponentID, Labels: map[string]string{
		domain.LabelComponent: "true", domain.LabelComponentRole: "edge", domain.LabelComponentGeneration: "1",
		domain.LabelComponentMigrationID: "fixture", domain.LabelComponentOwner: "migration",
	}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{edge}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test"}}}, nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, edge.Name, "gordon-app-fixture").Return(nil).Once()

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), command))
}

func TestRuntimeComponentLifecycleRejectsUnsafeAppNetworkConnections(t *testing.T) {
	identity := testRuntimeCommandIdentity("component-connect-unsafe")
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: identity, TargetComponentID: "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: domain.RuntimeComponentLifecycleConnect, InternalNetwork: "gordon-app-fixture", PreserveVolumes: true,
	}
	edge := &domain.Container{ID: "edge-id", Name: command.TargetComponentID, Labels: map[string]string{
		domain.LabelComponent: "true", domain.LabelComponentRole: "edge", domain.LabelComponentGeneration: "1",
		domain.LabelComponentMigrationID: "fixture", domain.LabelComponentOwner: "runtime",
	}}
	valid := &domain.NetworkInfo{Name: command.InternalNetwork, Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test"}}
	for name, networks := range map[string][]*domain.NetworkInfo{
		"unmanaged":       {{Name: command.InternalNetwork, Containers: valid.Containers}},
		"no target alias": {{Name: command.InternalNetwork, Labels: valid.Labels, Containers: []string{"app"}}},
		"internal":        {{Name: command.InternalNetwork, Internal: true, Labels: valid.Labels, Containers: valid.Containers}},
		"duplicate":       {valid, {Name: command.InternalNetwork, Labels: valid.Labels, Containers: valid.Containers}},
	} {
		t.Run(name, func(t *testing.T) {
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{edge}, nil).Once()
			runtime.EXPECT().ListNetworks(mock.Anything).Return(networks, nil).Once()
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
			err := manager.ApplyComponentLifecycle(context.Background(), command)
			require.Error(t, err)
		})
	}

	for _, unsafeName := range []string{"bridge", "host", "default", "gordon-internal-fixture-g1", "gordon-app/../private"} {
		t.Run("unsafe requested name "+unsafeName, func(t *testing.T) {
			runtime := outmocks.NewMockContainerRuntime(t)
			unsafeCommand := command
			unsafeCommand.InternalNetwork = unsafeName
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
			require.Error(t, manager.ApplyComponentLifecycle(context.Background(), unsafeCommand))
		})
	}
}

func TestRuntimeComponentLifecycleConnectRejectsNonEdgeRole(t *testing.T) {
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("component-connect-role"), TargetComponentID: "gordon-registry-fixture-g1", TargetComponentRole: domain.ComponentRoleRegistry,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: domain.RuntimeComponentLifecycleConnect, InternalNetwork: "gordon-app-fixture", PreserveVolumes: true,
	}
	manager := NewRuntimeComponentLifecycleManager(outmocks.NewMockContainerRuntime(t), RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	require.ErrorIs(t, manager.ApplyComponentLifecycle(context.Background(), command), ErrRuntimePolicyDenied)
}

func TestRuntimeComponentLifecycleConnectIsIdempotentWhenEdgeAlreadyAttached(t *testing.T) {
	identity := testRuntimeCommandIdentity("component-connect-idempotent")
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: identity, TargetComponentID: "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: domain.RuntimeComponentLifecycleConnect, InternalNetwork: "gordon-app-fixture", PreserveVolumes: true,
	}
	edge := &domain.Container{ID: "edge-id", Name: command.TargetComponentID, Labels: map[string]string{
		domain.LabelComponent: "true", domain.LabelComponentRole: "edge", domain.LabelComponentGeneration: "1",
		domain.LabelComponentMigrationID: "fixture", domain.LabelComponentOwner: "migration",
	}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{edge}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: command.InternalNetwork, Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{edge.Name, "gordon-target-app-example-test"}}}, nil).Once()

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), command))
}

func TestComponentPersistentVolumesGiveOnlyControlStableManagedSecrets(t *testing.T) {
	const volume = "gordon-control-secrets-0123456789abcdef"
	manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{ManagedControlSecretsVolume: volume}}
	base := domain.RuntimeSelfUpdateCommand{PolicyDecisionID: "migration:first"}
	base.Generation = 1
	base.TargetComponentRole = domain.ComponentRoleControl
	controlFirst := manager.componentPersistentVolumes(base)
	assert.Equal(t, volume, controlFirst[managedControlSecretsPath])

	base.PolicyDecisionID = "migration:second"
	base.Generation = 9
	assert.Equal(t, volume, manager.componentPersistentVolumes(base)[managedControlSecretsPath], "managed secret volume must survive generation updates")

	for _, role := range []domain.ComponentRole{domain.ComponentRoleRuntime, domain.ComponentRoleEdge, domain.ComponentRoleRegistry} {
		base.TargetComponentRole = role
		assert.NotContains(t, manager.componentPersistentVolumes(base), managedControlSecretsPath)
	}
}

func TestRuntimeComponentLifecycleRejectsInvalidManagedSecretsMountsOnExistingTargets(t *testing.T) {
	const configuredVolume = "gordon-control-secrets-0123456789abcdef"
	const foreignVolume = "gordon-control-secrets-fedcba9876543210"
	validMount := domain.ContainerVolumeMount{Name: configuredVolume, Type: "volume", Destination: managedControlSecretsPath}

	for _, test := range []struct {
		name   string
		role   domain.ComponentRole
		action domain.RuntimeComponentLifecycleAction
		mounts []domain.ContainerVolumeMount
		status string
	}{
		{name: "running control missing mount", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleStart, status: "running"},
		{name: "stopped control missing mount", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleStart, status: "exited"},
		{name: "control foreign source", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleStart, mounts: []domain.ContainerVolumeMount{{Name: foreignVolume, Type: "volume", Destination: managedControlSecretsPath}}, status: "running"},
		{name: "control read-only source", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleStart, mounts: []domain.ContainerVolumeMount{{Name: configuredVolume, Type: "volume", Destination: managedControlSecretsPath, ReadOnly: true}}, status: "running"},
		{name: "control duplicate source", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleStart, mounts: []domain.ContainerVolumeMount{validMount, validMount}, status: "running"},
		{name: "control alternate destination", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleStart, mounts: []domain.ContainerVolumeMount{{Name: configuredVolume, Type: "volume", Destination: "/tmp/secrets"}}, status: "running"},
		{name: "control foreign source at managed destination", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleStart, mounts: []domain.ContainerVolumeMount{{Name: "ordinary-volume", Type: "volume", Destination: managedControlSecretsPath}}, status: "running"},
		{name: "non-control managed source", role: domain.ComponentRoleRuntime, action: domain.RuntimeComponentLifecycleStart, mounts: []domain.ContainerVolumeMount{{Name: configuredVolume, Type: "volume", Destination: "/tmp/secrets"}}, status: "running"},
		{name: "health rejects before inspect", role: domain.ComponentRoleControl, action: domain.RuntimeComponentLifecycleHealth, mounts: []domain.ContainerVolumeMount{validMount, {Name: foreignVolume, Type: "volume", Destination: "/tmp/foreign"}}, status: "running"},
	} {
		t.Run(test.name, func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
			require.NoError(t, os.MkdirAll(configDir, 0o700))
			configPath := filepath.Join(configDir, string(test.role)+".toml")
			require.NoError(t, os.WriteFile(configPath, []byte("[component]\n"), 0o600))
			command := managedSecretsLifecycleCommand(test.role, test.action, configPath)
			container := &domain.Container{ID: "existing", Name: command.TargetComponentID, Status: test.status, Labels: componentLifecycleLabels(command), VolumeMounts: test.mounts}
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume})

			err := manager.ApplyComponentLifecycle(context.Background(), command)
			require.ErrorIs(t, err, ErrRuntimePolicyDenied)
		})
	}
}

func TestRuntimeComponentLifecycleAcceptsOnlyExactManagedControlMountForExistingStart(t *testing.T) {
	const configuredVolume = "gordon-control-secrets-0123456789abcdef"
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "control.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[control]\n"), 0o600))
	command := managedSecretsLifecycleCommand(domain.ComponentRoleControl, domain.RuntimeComponentLifecycleStart, configPath)

	for _, running := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "running"}[running], func(t *testing.T) {
			container := &domain.Container{ID: "existing", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command), VolumeMounts: []domain.ContainerVolumeMount{{Name: configuredVolume, Type: "volume", Destination: managedControlSecretsPath}}}
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
			runtime.EXPECT().IsContainerRunning(mock.Anything, container.ID).Return(running, nil).Once()
			if !running {
				runtime.EXPECT().StartContainer(mock.Anything, container.ID).Return(nil).Once()
			}
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume})
			require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), command))
		})
	}
}

func TestRuntimeComponentLifecycleRecoveryInventoryRejectsManagedSecretsOnNonControl(t *testing.T) {
	const configuredVolume = "gordon-control-secrets-0123456789abcdef"
	command := managedSecretsLifecycleCommand(domain.ComponentRoleEdge, domain.RuntimeComponentLifecycleActivate, "/not-used")
	target := &domain.Container{ID: "target", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command), VolumeMounts: []domain.ContainerVolumeMount{{Name: configuredVolume, Type: "volume", Destination: "/tmp/secrets"}}}
	manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume}}

	_, _, err := manager.cutoverInventory(command, []*domain.Container{target})
	require.ErrorIs(t, err, ErrRuntimePolicyDenied)
}

func managedSecretsLifecycleCommand(role domain.ComponentRole, action domain.RuntimeComponentLifecycleAction, configPath string) domain.RuntimeSelfUpdateCommand {
	identity := testRuntimeCommandIdentity("managed-secrets-existing")
	identity.SourceComponentID = "gordon-control"
	return domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: identity,
		TargetComponentID:      "gordon-" + string(role) + "-fixture-g1",
		TargetComponentRole:    role,
		TargetVersion:          "v2",
		Policy:                 domain.RuntimeSelfUpdatePolicyManualApproval,
		PolicyDecisionID:       "migration:fixture",
		LifecycleAction:        action,
		DesiredImage:           "example.invalid/gordon:v2",
		DesiredStateHash:       "fixture-hash",
		InternalNetwork:        "gordon-internal-fixture-g1",
		ConfigFile:             configPath,
		OldServingComponentID:  "old-monolith",
		FinalPortPublishes: []domain.ContainerPortPublish{
			{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP},
		},
		PreserveVolumes: true,
	}
}

func TestControlComponentConfigRejectsMissingOrMalformedManagedSecretsVolume(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "control.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[control]\n"), 0o600))
	command := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: testRuntimeCommandIdentity("control-secrets"), TargetComponentID: "gordon-control-fixture-g1", TargetComponentRole: domain.ComponentRoleControl, TargetVersion: "v2", PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: configPath}
	command.SourceComponentID = "gordon-control"
	for _, volume := range []string{"", "gordon-control-secrets-invalid"} {
		manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: volume}}
		_, err := manager.componentConfig(command, nil)
		require.ErrorIs(t, err, ErrRuntimePolicyDenied)
	}
}

func TestComponentLifecycleMountsOnlyPrivateMigrationSocketStateForRuntimeAndControl(t *testing.T) {
	data := t.TempDir()
	configDir := filepath.Join(data, "migration", "config", "fixture", "1")
	envDir := filepath.Join(data, "migration", "env", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.MkdirAll(envDir, 0o700))
	configPath := filepath.Join(configDir, "runtime.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[runtime]\n"), 0o600))
	manager := NewRuntimeComponentLifecycleManager(outmocks.NewMockContainerRuntime(t), RuntimePolicy{Mode: RuntimePolicyModeEnforce, MigrationStateRoot: filepath.Join(data, "migration"), ManagedControlSecretsVolume: "gordon-control-secrets-0123456789abcdef"}).(*runtimeComponentLifecycleManager)
	identity := testRuntimeCommandIdentity("component-socket-state")
	identity.SourceComponentID = "gordon-control"
	command := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-runtime-fixture-g1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture-hash", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: configPath, PreserveVolumes: true}
	config, err := manager.componentConfig(command, nil)
	require.NoError(t, err)
	state := filepath.Join(data, "migration", "fixture")
	stateInfo, statErr := os.Stat(state)
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o700), stateInfo.Mode().Perm())
	assert.Equal(t, state, config.Volumes["/var/lib/gordon/migration/fixture"])
	assert.NotContains(t, config.ReadOnlyVolumes, "/var/lib/gordon/migration/fixture")
	assert.Equal(t, filepath.Join(data, "migration", "config"), config.ReadOnlyVolumes[filepath.Join(data, "migration", "config")], "runtime alone needs a read-only host-path view to validate later role manifests")
	assert.Equal(t, filepath.Join(data, "migration", "env"), config.ReadOnlyVolumes[filepath.Join(data, "migration", "env")], "runtime alone needs a read-only host-path view to load later role environments")
	assert.NotContains(t, config.Volumes, "/run/gordon/runtime.sock")

	command.TargetComponentRole = domain.ComponentRoleControl
	command.TargetComponentID = "gordon-control-fixture-g1"
	config, err = manager.componentConfig(command, nil)
	require.NoError(t, err)
	assert.Equal(t, state, config.ReadOnlyVolumes["/var/lib/gordon/migration/fixture"])
	assert.NotContains(t, config.Volumes, "/var/lib/gordon/migration/fixture")
	// Control gets a writable attestation subdirectory, not write access to
	// the runtime socket parent. This is the only cross-process migration
	// state it may durably update after authenticated edge acknowledgement.
	attestation := filepath.Join(state, "attestation")
	attestationInfo, attestationErr := os.Stat(attestation)
	require.NoError(t, attestationErr)
	assert.Equal(t, os.FileMode(0o700), attestationInfo.Mode().Perm())
	assert.Equal(t, attestation, config.Volumes["/var/lib/gordon/migration/fixture/attestation"])
	assert.NotContains(t, config.ReadOnlyVolumes, filepath.Join(data, "migration", "config"))
	assert.NotContains(t, config.ReadOnlyVolumes, filepath.Join(data, "migration", "env"))

	command.TargetComponentRole = domain.ComponentRoleEdge
	command.TargetComponentID = "gordon-edge-fixture-g1"
	config, err = manager.componentConfig(command, nil)
	require.NoError(t, err)
	assert.NotContains(t, config.Volumes, "/var/lib/gordon/migration/fixture")
	assert.NotContains(t, config.ReadOnlyVolumes, "/var/lib/gordon/migration/fixture")
}
