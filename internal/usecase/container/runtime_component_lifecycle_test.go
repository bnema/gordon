package container

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/pkg/cap"
	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dockeradapter "github.com/bnema/gordon/internal/adapters/out/docker"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func componentKeepIDMode(identity domain.ComponentProcessIdentity) string {
	return "keep-id:uid=" + strconv.Itoa(identity.UID) + ",gid=" + strconv.Itoa(identity.GID)
}

func testLifecycleConfig(t *testing.T, role domain.ComponentRole) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, string(role)+".toml")
	require.NoError(t, os.WriteFile(path, []byte("[component]\n"), 0o600))
	return path
}

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

func applyTestComponentLifecycle(manager RuntimeComponentLifecycleManager, ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	requirement, ok := domain.RuntimeComponentLifecycleRequirement(command.LifecycleAction)
	if !ok {
		return manager.ApplyComponentLifecycle(ctx, command)
	}
	switch requirement.ProfileMode {
	case domain.RuntimeComponentLifecycleProfileIdentityOnly:
		readCommand, err := domain.NewRuntimeComponentLifecycleReadCommand(
			command.RuntimeCommandIdentity, command.TargetComponentID, command.TargetComponentRole,
			command.PolicyDecisionID, command.LifecycleAction,
		)
		if err != nil {
			return err
		}
		command = readCommand
	case domain.RuntimeComponentLifecycleProfileFull:
		profile, profileOK := domain.FixedRuntimeComponentLifecycleProfile(command.TargetComponentRole)
		if profileOK {
			command.LifecycleProfile = profile
		}
	case domain.RuntimeComponentLifecycleProfileNone:
		command.LifecycleProfile = domain.RuntimeComponentLifecycleProfile{}
	}
	return manager.ApplyComponentLifecycle(ctx, command)
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
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-network-fixture-g1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleEnsureNetwork, InternalNetwork: "gordon-internal-fixture-g1", PreserveVolumes: true}))
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-control-fixture-g1", TargetComponentRole: domain.ComponentRoleControl, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture-hash", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: configPath, PreserveVolumes: true}))
}

func TestRuntimeComponentLifecycleConnectsPreparedEdgeOnlyToValidatedManagedAppNetwork(t *testing.T) {
	identity := testRuntimeCommandIdentity("component-connect")
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: identity, TargetComponentID: "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: domain.RuntimeComponentLifecycleConnect, InternalNetwork: "gordon-app-fixture", PreserveVolumes: true,
		ConfigFile: testLifecycleConfig(t, domain.ComponentRoleEdge),
	}
	edge := edgeLifecycleFixture(command.ConfigFile, &domain.Container{ID: "edge-id", Name: command.TargetComponentID, Labels: map[string]string{
		domain.LabelComponent: "true", domain.LabelComponentRole: "edge", domain.LabelComponentGeneration: "1",
		domain.LabelComponentMigrationID: "fixture", domain.LabelComponentOwner: "migration",
	}})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{edge}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, edge.ID).Return(edge, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test"}}}, nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, edge.Name, "gordon-app-fixture").Return(nil).Once()

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), command))
}

func TestRuntimeComponentLifecycleRejectsUnsafeAppNetworkConnections(t *testing.T) {
	identity := testRuntimeCommandIdentity("component-connect-unsafe")
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: identity, TargetComponentID: "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: domain.RuntimeComponentLifecycleConnect, InternalNetwork: "gordon-app-fixture", PreserveVolumes: true,
		ConfigFile: testLifecycleConfig(t, domain.ComponentRoleEdge),
	}
	edge := edgeLifecycleFixture(command.ConfigFile, &domain.Container{ID: "edge-id", Name: command.TargetComponentID, Labels: map[string]string{
		domain.LabelComponent: "true", domain.LabelComponentRole: "edge", domain.LabelComponentGeneration: "1",
		domain.LabelComponentMigrationID: "fixture", domain.LabelComponentOwner: "runtime",
	}})
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
			runtime.EXPECT().InspectContainer(mock.Anything, edge.ID).Return(edge, nil).Once()
			runtime.EXPECT().ListNetworks(mock.Anything).Return(networks, nil).Once()
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
			err := applyTestComponentLifecycle(manager, context.Background(), command)
			require.Error(t, err)
		})
	}

	for _, unsafeName := range []string{"bridge", "host", "default", "gordon-internal-fixture-g1", "gordon-app/../private"} {
		t.Run("unsafe requested name "+unsafeName, func(t *testing.T) {
			runtime := outmocks.NewMockContainerRuntime(t)
			unsafeCommand := command
			unsafeCommand.InternalNetwork = unsafeName
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
			require.Error(t, applyTestComponentLifecycle(manager, context.Background(), unsafeCommand))
		})
	}
}

func TestRuntimeComponentLifecycleConnectRejectsNonEdgeRole(t *testing.T) {
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("component-connect-role"), TargetComponentID: "gordon-registry-fixture-g1", TargetComponentRole: domain.ComponentRoleRegistry,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: domain.RuntimeComponentLifecycleConnect, InternalNetwork: "gordon-app-fixture", PreserveVolumes: true,
		ConfigFile: testLifecycleConfig(t, domain.ComponentRoleEdge),
	}
	manager := NewRuntimeComponentLifecycleManager(outmocks.NewMockContainerRuntime(t), RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	require.ErrorIs(t, applyTestComponentLifecycle(manager, context.Background(), command), ErrRuntimePolicyDenied)
}

func TestRuntimeComponentLifecycleConnectIsIdempotentWhenEdgeAlreadyAttached(t *testing.T) {
	identity := testRuntimeCommandIdentity("component-connect-idempotent")
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: identity, TargetComponentID: "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture",
		LifecycleAction: domain.RuntimeComponentLifecycleConnect, InternalNetwork: "gordon-app-fixture", PreserveVolumes: true,
		ConfigFile: testLifecycleConfig(t, domain.ComponentRoleEdge),
	}
	edge := edgeLifecycleFixture(command.ConfigFile, &domain.Container{ID: "edge-id", Name: command.TargetComponentID, Labels: map[string]string{
		domain.LabelComponent: "true", domain.LabelComponentRole: "edge", domain.LabelComponentGeneration: "1",
		domain.LabelComponentMigrationID: "fixture", domain.LabelComponentOwner: "migration",
	}})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{edge}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, edge.ID).Return(edge, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: command.InternalNetwork, Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{edge.Name, "gordon-target-app-example-test"}}}, nil).Once()

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), command))
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
			runtime.EXPECT().InspectContainer(mock.Anything, container.ID).Return(container, nil).Once()
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume})

			err := applyTestComponentLifecycle(manager, context.Background(), command)
			require.ErrorIs(t, err, ErrRuntimePolicyDenied)
		})
	}
}

func exactLifecycleContainer(t *testing.T, manager *runtimeComponentLifecycleManager, command domain.RuntimeSelfUpdateCommand, container *domain.Container) *domain.Container {
	t.Helper()
	identity, ok := domain.FixedComponentProcessIdentity(command.TargetComponentRole)
	require.True(t, ok)
	container.User = identity.User
	container.UsernsMode = componentKeepIDMode(identity)
	container.CapDrop = []string{"ALL"}
	container.NoNewPrivileges = true
	expected, err := manager.expectedLifecycleMounts(command, command.PortPublishes)
	require.NoError(t, err)
	container.VolumeMounts = nil
	for destination, mount := range expected {
		actual := domain.ContainerVolumeMount{Destination: destination, Options: mount.options, ReadOnly: mount.readOnly}
		if filepath.IsAbs(mount.source) {
			actual.Type, actual.Source = "bind", mount.source
		} else {
			actual.Type, actual.Name = "volume", mount.source
		}
		container.VolumeMounts = append(container.VolumeMounts, actual)
	}
	return container
}

func TestRuntimeComponentLifecycleReadUsesAuthoritativeExistingProfile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "migration")
	configPath := filepath.Join(root, "config", "fixture", "1", "edge.toml")
	require.NoError(t, os.MkdirAll(filepath.Dir(configPath), 0o700))
	require.NoError(t, os.WriteFile(configPath, []byte("[edge]\n"), 0o600))
	identity, ok := domain.FixedComponentProcessIdentity(domain.ComponentRoleEdge)
	require.True(t, ok)
	command := domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: testRuntimeCommandIdentity("minimal-health"), TargetComponentID: "gordon-edge-fixture-g1",
		TargetComponentRole: domain.ComponentRoleEdge, PolicyDecisionID: "migration:fixture",
		LifecycleAction:  domain.RuntimeComponentLifecycleHealth,
		LifecycleProfile: domain.RuntimeComponentLifecycleProfile{ProcessIdentity: identity},
	}
	valid := &domain.Container{
		ID: "existing", Name: command.TargetComponentID, User: identity.User,
		UsernsMode: componentKeepIDMode(identity), CapDrop: []string{"ALL"}, NoNewPrivileges: true,
		Labels: map[string]string{
			domain.LabelComponent: "true", domain.LabelComponentRole: string(domain.ComponentRoleEdge),
			domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture",
			domain.LabelComponentOwner: "runtime", domain.LabelComponentDesiredStateHash: "authoritative-hash",
		},
		VolumeMounts: []domain.ContainerVolumeMount{{Type: "bind", Source: configPath, Destination: "/etc/gordon/role.toml", ReadOnly: true}},
	}

	for _, test := range []struct {
		name   string
		mutate func(*domain.Container)
		valid  bool
	}{
		{name: "exact profile", valid: true},
		{name: "wrong security", mutate: func(container *domain.Container) { container.NoNewPrivileges = false }},
		{name: "extra mount", mutate: func(container *domain.Container) {
			container.VolumeMounts = append(container.VolumeMounts, domain.ContainerVolumeMount{Type: "bind", Source: "/tmp/foreign", Destination: "/tmp/foreign"})
		}},
		{name: "wrong role", mutate: func(container *domain.Container) {
			container.Labels[domain.LabelComponentRole] = string(domain.ComponentRoleControl)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspected := *valid
			inspected.Labels = map[string]string{}
			for key, value := range valid.Labels {
				inspected.Labels[key] = value
			}
			inspected.VolumeMounts = append([]domain.ContainerVolumeMount(nil), valid.VolumeMounts...)
			if test.mutate != nil {
				test.mutate(&inspected)
			}
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{{ID: inspected.ID, Name: inspected.Name}}, nil).Once()
			runtime.EXPECT().InspectContainer(mock.Anything, inspected.ID).Return(&inspected, nil).Once()
			if test.valid {
				runtime.EXPECT().IsContainerRunning(mock.Anything, inspected.ID).Return(true, nil).Once()
				runtime.EXPECT().GetContainerHealthStatus(mock.Anything, inspected.ID).Return("healthy", true, nil).Once()
			}
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, MigrationStateRoot: root})
			err := manager.ApplyComponentLifecycle(context.Background(), command)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrRuntimePolicyDenied)
			}
		})
	}
}

func TestRuntimeComponentLifecycleUsesAuthoritativeInspectForSparseHealthyRetry(t *testing.T) {
	const configuredVolume = "gordon-control-secrets-0123456789abcdef"
	configPath := testLifecycleConfig(t, domain.ComponentRoleControl)
	command := managedSecretsLifecycleCommand(domain.ComponentRoleControl, domain.RuntimeComponentLifecycleHealth, configPath)
	runtime := outmocks.NewMockContainerRuntime(t)
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume}).(*runtimeComponentLifecycleManager)
	inspected := exactLifecycleContainer(t, manager, command, &domain.Container{ID: "existing", Name: command.TargetComponentID, Status: "running", Labels: componentLifecycleLabels(command)})

	// Docker-compatible list responses do not contain process identity fields.
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{{ID: inspected.ID, Name: inspected.Name}}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, inspected.ID).Return(inspected, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, inspected.ID).Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, inspected.ID).Return("healthy", true, nil).Once()

	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), command))
}

func TestRuntimeComponentLifecycleDockerAdapterInspectsSparseCandidates(t *testing.T) {
	configPath := testLifecycleConfig(t, domain.ComponentRoleEdge)
	command := managedSecretsLifecycleCommand(domain.ComponentRoleEdge, domain.RuntimeComponentLifecycleHealth, configPath)

	for _, test := range []struct {
		name          string
		inspectedName string
		wantDenied    bool
	}{
		{name: "plausible inspect lacks native proof", inspectedName: command.TargetComponentID, wantDenied: true},
		{name: "forged inspect mismatch", inspectedName: "foreign", wantDenied: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch request.URL.Path {
				case "/v1.41/containers/json":
					_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "existing", "Names": []string{"/" + command.TargetComponentID}, "State": "running"}})
				case "/v1.41/containers/existing/json":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"Id": "existing", "Name": "/" + test.inspectedName, "Created": "2026-05-05T00:00:00Z",
						"Config":          map[string]any{"Image": "example.invalid/gordon:v2", "User": "21003:21003", "Labels": componentLifecycleLabels(command)},
						"HostConfig":      map[string]any{"UsernsMode": "keep-id:uid=21003,gid=21003", "CapDrop": cap.Known(), "CapAdd": []string{}, "SecurityOpt": []string{"no-new-privileges:true"}},
						"State":           map[string]any{"Status": "running", "ExitCode": 0},
						"Mounts":          []map[string]any{{"Type": "bind", "Source": configPath, "Destination": "/etc/gordon/role.toml", "RW": false}},
						"NetworkSettings": map[string]any{"Ports": map[string]any{}},
					})
				default:
					http.NotFound(w, request)
				}
			}))
			defer server.Close()

			host := strings.TrimPrefix(server.URL, "http://")
			apiClient, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
			require.NoError(t, err)
			manager := NewRuntimeComponentLifecycleManager(dockeradapter.NewRuntimeWithClient(apiClient), RuntimePolicy{Mode: RuntimePolicyModeEnforce})
			err = applyTestComponentLifecycle(manager, context.Background(), command)
			if test.wantDenied {
				require.ErrorIs(t, err, ErrRuntimePolicyDenied)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRuntimeComponentLifecycleFailsClosedWhenCandidateInspectFails(t *testing.T) {
	configPath := testLifecycleConfig(t, domain.ComponentRoleEdge)
	command := managedSecretsLifecycleCommand(domain.ComponentRoleEdge, domain.RuntimeComponentLifecycleStop, configPath)
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{{ID: "existing", Name: command.TargetComponentID}}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, "existing").Return(nil, assert.AnError).Once()

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	require.Error(t, applyTestComponentLifecycle(manager, context.Background(), command))
}

func TestRuntimeComponentLifecycleRejectsForgedSparseCandidateAfterInspectMismatch(t *testing.T) {
	const configuredVolume = "gordon-control-secrets-0123456789abcdef"
	configPath := testLifecycleConfig(t, domain.ComponentRoleControl)
	command := managedSecretsLifecycleCommand(domain.ComponentRoleControl, domain.RuntimeComponentLifecycleStart, configPath)
	runtime := outmocks.NewMockContainerRuntime(t)
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume}).(*runtimeComponentLifecycleManager)
	forged := exactLifecycleContainer(t, manager, command, &domain.Container{ID: "forged", Name: "foreign", Labels: componentLifecycleLabels(command)})

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{{ID: forged.ID, Name: command.TargetComponentID}}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, forged.ID).Return(forged, nil).Once()

	require.ErrorIs(t, applyTestComponentLifecycle(manager, context.Background(), command), ErrRuntimePolicyDenied)
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
			runtime := outmocks.NewMockContainerRuntime(t)
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume}).(*runtimeComponentLifecycleManager)
			container := exactLifecycleContainer(t, manager, command, &domain.Container{ID: "existing", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command)})
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
			runtime.EXPECT().InspectContainer(mock.Anything, container.ID).Return(container, nil).Once()
			runtime.EXPECT().IsContainerRunning(mock.Anything, container.ID).Return(running, nil).Once()
			if !running {
				runtime.EXPECT().StartContainer(mock.Anything, container.ID).Return(nil).Once()
			}
			require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), command))
		})
	}
}

func TestRuntimeComponentLifecycleRecoveryInventoryRejectsManagedSecretsOnNonControl(t *testing.T) {
	const configuredVolume = "gordon-control-secrets-0123456789abcdef"
	command := managedSecretsLifecycleCommand(domain.ComponentRoleEdge, domain.RuntimeComponentLifecycleActivate, "/not-used")
	target := &domain.Container{ID: "target", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command), VolumeMounts: []domain.ContainerVolumeMount{{Name: configuredVolume, Type: "volume", Destination: "/tmp/secrets"}}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, target.ID).Return(target, nil).Once()
	manager := &runtimeComponentLifecycleManager{runtime: runtime, policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: configuredVolume}}

	_, _, err := manager.cutoverInventory(context.Background(), command, []*domain.Container{target})
	require.ErrorIs(t, err, ErrRuntimePolicyDenied)
}

func managedSecretsLifecycleCommand(role domain.ComponentRole, action domain.RuntimeComponentLifecycleAction, configPath string) domain.RuntimeSelfUpdateCommand {
	identity := testRuntimeCommandIdentity("managed-secrets-existing")
	identity.SourceComponentID = "gordon-control"
	profile, _ := domain.FixedRuntimeComponentLifecycleProfile(role)
	return domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: identity,
		TargetComponentID:      "gordon-" + string(role) + "-fixture-g1",
		TargetComponentRole:    role,
		TargetVersion:          "v2",
		Policy:                 domain.RuntimeSelfUpdatePolicyManualApproval,
		PolicyDecisionID:       "migration:fixture",
		LifecycleAction:        action,
		LifecycleProfile:       profile,
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
	envPath := filepath.Join(envDir, "runtime.env")
	require.NoError(t, os.WriteFile(configPath, []byte("[runtime]\n"), 0o600))
	require.NoError(t, os.WriteFile(envPath, []byte("DOCKER_HOST=unix:///run/user/1000/podman/podman.sock\n"), 0o600))
	manager := NewRuntimeComponentLifecycleManager(outmocks.NewMockContainerRuntime(t), RuntimePolicy{Mode: RuntimePolicyModeEnforce, MigrationStateRoot: filepath.Join(data, "migration"), ManagedControlSecretsVolume: "gordon-control-secrets-0123456789abcdef"}).(*runtimeComponentLifecycleManager)
	identity := testRuntimeCommandIdentity("component-socket-state")
	identity.SourceComponentID = "gordon-control"
	runtimeProfile, _ := domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleRuntime)
	command := domain.RuntimeSelfUpdateCommand{RuntimeCommandIdentity: identity, TargetComponentID: "gordon-runtime-fixture-g1", TargetComponentRole: domain.ComponentRoleRuntime, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, LifecycleProfile: runtimeProfile, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture-hash", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: configPath, EnvironmentFile: envPath, PreserveVolumes: true}
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
	assert.Equal(t, "/run/user/1000/podman/podman.sock", config.ReadOnlyVolumes["/run/gordon/runtime.sock"])
	assert.Contains(t, config.Env, "DOCKER_HOST=unix:///run/gordon/runtime.sock")

	command.TargetComponentRole = domain.ComponentRoleControl
	command.LifecycleProfile, _ = domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleControl)
	command.TargetComponentID = "gordon-control-fixture-g1"
	command.ConfigFile = filepath.Join(configDir, "control.toml")
	command.EnvironmentFile = ""
	require.NoError(t, os.WriteFile(command.ConfigFile, []byte("[control]\n"), 0o600))
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
	command.LifecycleProfile, _ = domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleEdge)
	command.TargetComponentID = "gordon-edge-fixture-g1"
	command.ConfigFile = filepath.Join(configDir, "edge.toml")
	require.NoError(t, os.WriteFile(command.ConfigFile, []byte("[edge]\n"), 0o600))
	config, err = manager.componentConfig(command, nil)
	require.NoError(t, err)
	assert.NotContains(t, config.Volumes, "/var/lib/gordon/migration/fixture")
	assert.NotContains(t, config.ReadOnlyVolumes, "/var/lib/gordon/migration/fixture")
}
