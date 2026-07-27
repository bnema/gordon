package container

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/docker/docker/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dockeradapter "github.com/bnema/gordon/internal/adapters/out/docker"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestComponentGenerationVolumeNameUsesRoleMigrationAndGeneration(t *testing.T) {
	assert.Equal(t, "gordon-control-fixture-g7", componentGenerationVolumeName(domain.ComponentRoleControl, "fixture", 7))
}

func TestRuntimeComponentLifecycleUsesExactRootlessIdentityForEveryRole(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: "gordon-control-secrets-0123456789abcdef"}}

	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry, domain.ComponentRoleEdge} {
		t.Run(string(role), func(t *testing.T) {
			configPath := filepath.Join(configDir, string(role)+".toml")
			require.NoError(t, os.WriteFile(configPath, []byte("[component]\n"), 0o600))
			command := managedSecretsLifecycleCommand(role, domain.RuntimeComponentLifecycleStart, configPath)
			if role == domain.ComponentRoleRuntime {
				envDir := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(configPath)))), "env", "fixture", "1")
				require.NoError(t, os.MkdirAll(envDir, 0o700))
				command.EnvironmentFile = filepath.Join(envDir, "runtime.env")
				require.NoError(t, os.WriteFile(command.EnvironmentFile, []byte("DOCKER_HOST=unix:///run/user/1000/podman/podman.sock\n"), 0o600))
			}
			config, err := manager.componentConfig(command, nil)
			require.NoError(t, err)
			identity, ok := domain.FixedComponentProcessIdentity(role)
			require.True(t, ok)
			assert.Equal(t, identity.User, config.User)
			assert.Equal(t, "keep-id:uid="+strconv.Itoa(identity.UID)+",gid="+strconv.Itoa(identity.GID), config.UsernsMode)
			if role == domain.ComponentRoleEdge {
				assert.Empty(t, config.VolumeOptions)
			} else {
				assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, config.VolumeOptions["/var/lib/gordon"])
				assert.Len(t, config.VolumeOptions, 1, "only the role generation named volume may receive U")
			}
			for destination := range config.ReadOnlyVolumes {
				assert.NotContains(t, config.VolumeOptions, destination)
			}
			assert.NotContains(t, config.VolumeOptions, managedControlSecretsPath)
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
		"missing capability drop":   func(c *domain.Container) { c.CapDrop = nil },
		"added capability":          func(c *domain.Container) { c.CapAdd = []string{"NET_BIND_SERVICE"} },
		"missing no-new-privileges": func(c *domain.Container) { c.NoNewPrivileges = false },
		"missing managed mount":     func(c *domain.Container) { c.VolumeMounts = nil },
		"writable config":           func(c *domain.Container) { c.VolumeMounts[0].ReadOnly = false },
		"duplicate config":          func(c *domain.Container) { c.VolumeMounts = append(c.VolumeMounts, c.VolumeMounts[0]) },
		"foreign config":            func(c *domain.Container) { c.VolumeMounts[0].Source = filepath.Join(configDir, "foreign.toml") },
		"cross-role generation volume": func(c *domain.Container) {
			c.VolumeMounts = append(c.VolumeMounts, domain.ContainerVolumeMount{Type: "volume", Name: "gordon-runtime-fixture-g1", Destination: "/var/lib/gordon"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			container := valid
			container.CapDrop = append([]string(nil), valid.CapDrop...)
			container.CapAdd = append([]string(nil), valid.CapAdd...)
			container.VolumeMounts = append([]domain.ContainerVolumeMount(nil), valid.VolumeMounts...)
			forge(&container)
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{&container}, nil).Once()
			runtime.EXPECT().InspectContainer(mock.Anything, container.ID).Return(&container, nil).Once()
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
			require.ErrorIs(t, applyTestComponentLifecycle(manager, t.Context(), command), ErrRuntimePolicyDenied)
		})
	}
}

func TestRuntimePolicyAllowsUOnlyOnExactRoleGenerationVolume(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "control.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[control]\n"), 0o600))
	identity := testRuntimeCommandIdentity("volume-options")
	identity.SourceComponentID = "gordon-control"
	policy := RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedControlSecretsVolume: "gordon-control-secrets-0123456789abcdef"}
	manager := &runtimeComponentLifecycleManager{policy: policy}
	command := managedSecretsLifecycleCommand(domain.ComponentRoleControl, domain.RuntimeComponentLifecycleStart, configPath)
	valid, err := manager.componentConfig(command, nil)
	require.NoError(t, err)
	require.NoError(t, policy.CheckContainerConfig(identity, "", *valid))

	for name, mutate := range map[string]func(*domain.ContainerConfig){
		"missing option": func(config *domain.ContainerConfig) { config.VolumeOptions = nil },
		"wrong option": func(config *domain.ContainerConfig) {
			config.VolumeOptions["/var/lib/gordon"] = []string{"z"}
		},
		"extra option": func(config *domain.ContainerConfig) {
			config.VolumeOptions["/var/lib/gordon"] = []string{domain.ContainerVolumeOptionChown, "z"}
		},
		"option on secret volume": func(config *domain.ContainerConfig) {
			config.VolumeOptions[managedControlSecretsPath] = []string{domain.ContainerVolumeOptionChown}
		},
		"cross-role volume": func(config *domain.ContainerConfig) {
			config.Volumes["/var/lib/gordon"] = "gordon-runtime-fixture-g1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := *valid
			config.Volumes = maps.Clone(valid.Volumes)
			config.VolumeOptions = cloneVolumeOptions(valid.VolumeOptions)
			mutate(&config)
			require.ErrorIs(t, policy.CheckContainerConfig(identity, "", config), ErrRuntimePolicyDenied)
		})
	}

	for name, config := range map[string]domain.ContainerConfig{
		"ordinary workload": {Name: "gordon-app-example", Volumes: map[string]string{"/data": "app-data"}, VolumeOptions: map[string][]string{"/data": {domain.ContainerVolumeOptionChown}}},
		"monolith":          {Name: "gordon", Volumes: map[string]string{"/var/lib/gordon": "monolith-data"}, VolumeOptions: map[string][]string{"/var/lib/gordon": {domain.ContainerVolumeOptionChown}}},
		"host bind":         {Volumes: map[string]string{"/data": "/var/lib/gordon/data"}, VolumeOptions: map[string][]string{"/data": {domain.ContainerVolumeOptionChown}}},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, policy.CheckContainerConfig(identity, "", config), ErrRuntimePolicyDenied)
		})
	}
}

func TestRuntimeComponentLifecycleExistingGenerationVolumeRequiresExactU(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "control.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[control]\n"), 0o600))
	command := managedSecretsLifecycleCommand(domain.ComponentRoleControl, domain.RuntimeComponentLifecycleHealth, configPath)
	identity, _ := domain.FixedComponentProcessIdentity(domain.ComponentRoleControl)
	valid := &domain.Container{
		ID: "existing", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command), User: identity.User,
		UsernsMode: componentKeepIDMode(identity), CapDrop: []string{"ALL"}, NoNewPrivileges: true,
		VolumeMounts: []domain.ContainerVolumeMount{
			{Type: "bind", Source: configPath, Destination: "/etc/gordon/role.toml", ReadOnly: true},
			{Type: "volume", Name: "gordon-control-fixture-g1", Destination: "/var/lib/gordon", Options: []string{domain.ContainerVolumeOptionChown}},
		},
	}
	manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce}}
	require.NoError(t, manager.validateExistingLifecycleMounts(valid, command))
	for name, options := range map[string][]string{
		"missing": nil,
		"wrong":   {"z"},
		"extra":   {domain.ContainerVolumeOptionChown, "z"},
	} {
		t.Run(name, func(t *testing.T) {
			container := *valid
			container.VolumeMounts = append([]domain.ContainerVolumeMount(nil), valid.VolumeMounts...)
			container.VolumeMounts[1].Options = options
			require.ErrorIs(t, manager.validateExistingLifecycleMounts(&container, command), ErrRuntimePolicyDenied)
		})
	}
}

func TestRuntimeComponentLifecycleHealthAndReuseAcceptPodmanHostBindChown(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "control.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[control]\n"), 0o600))
	command := managedSecretsLifecycleCommand(domain.ComponentRoleControl, domain.RuntimeComponentLifecycleHealth, configPath)
	identity, ok := domain.FixedComponentProcessIdentity(domain.ComponentRoleControl)
	require.True(t, ok)

	volumeName := componentGenerationVolumeName(command.TargetComponentRole, strings.TrimPrefix(command.PolicyDecisionID, "migration:"), command.Generation)
	inspectFixture := map[string]any{
		"Id":      "existing",
		"Name":    "/" + command.TargetComponentID,
		"Image":   "sha256:fixture",
		"Created": "2026-05-05T00:00:00Z",
		"Config": map[string]any{
			"Image":  "gordon:fixture",
			"User":   identity.User,
			"Labels": componentLifecycleLabels(command),
		},
		"HostConfig": map[string]any{
			"Binds":         []string{configPath + ":/etc/gordon/role.toml:ro", volumeName + ":/var/lib/gordon:U"},
			"UsernsMode":    componentKeepIDMode(identity),
			"CapDrop":       []string{"ALL"},
			"CapAdd":        []string{},
			"SecurityOpt":   []string{"no-new-privileges:true"},
			"RestartPolicy": map[string]any{},
		},
		"State": map[string]any{"Status": "running", "ExitCode": 0},
		"Mounts": []map[string]any{
			{"Type": "bind", "Source": configPath, "Destination": "/etc/gordon/role.toml", "Mode": "ro", "RW": false, "Propagation": "rprivate"},
			{"Type": "volume", "Name": volumeName, "Source": "/home/fixture/.local/share/containers/storage/volumes/" + volumeName + "/_data", "Destination": "/var/lib/gordon", "Driver": "local", "Mode": "", "RW": true, "Propagation": "rprivate"},
		},
		"NetworkSettings": map[string]any{"Ports": map[string]any{}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		require.Equal(t, http.MethodGet, request.Method)
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1.41/containers/json":
			require.NoError(t, json.NewEncoder(w).Encode([]map[string]any{{
				"Id": "existing", "Names": []string{"/" + command.TargetComponentID}, "Image": "gordon:fixture", "ImageID": "sha256:fixture", "State": "running", "Labels": componentLifecycleLabels(command),
			}}))
		case "/v1.41/containers/existing/json":
			require.NoError(t, json.NewEncoder(w).Encode(inspectFixture))
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	apiClient, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
	require.NoError(t, err)
	inspected, err := dockeradapter.NewRuntimeWithClient(apiClient).InspectContainer(t.Context(), "existing")
	require.NoError(t, err)
	require.Len(t, inspected.VolumeMounts, 2)
	assert.True(t, inspected.VolumeMounts[0].ReadOnly)
	assert.Empty(t, inspected.VolumeMounts[0].Options, "ro is mount access metadata, not an engine-specific option")
	assert.Equal(t, []string{domain.ContainerVolumeOptionChown}, inspected.VolumeMounts[1].Options)

	manager := NewRuntimeComponentLifecycleManager(dockeradapter.NewRuntimeWithClient(apiClient), RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	for _, action := range []domain.RuntimeComponentLifecycleAction{domain.RuntimeComponentLifecycleHealth, domain.RuntimeComponentLifecycleStart} {
		t.Run(string(action), func(t *testing.T) {
			actionCommand := managedSecretsLifecycleCommand(domain.ComponentRoleControl, action, configPath)
			require.NoError(t, applyTestComponentLifecycle(manager, t.Context(), actionCommand))
		})
	}
}

func TestRuntimeComponentLifecycleRejectsUntrustedPodmanHostBindChown(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "control.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[control]\n"), 0o600))
	command := managedSecretsLifecycleCommand(domain.ComponentRoleControl, domain.RuntimeComponentLifecycleHealth, configPath)
	identity, ok := domain.FixedComponentProcessIdentity(domain.ComponentRoleControl)
	require.True(t, ok)
	volumeName := componentGenerationVolumeName(command.TargetComponentRole, strings.TrimPrefix(command.PolicyDecisionID, "migration:"), command.Generation)

	for name, binds := range map[string][]string{
		"missing U despite owned writable mount": {configPath + ":/etc/gordon/role.toml:ro", volumeName + ":/var/lib/gordon:rw"},
		"malformed U":                            {configPath + ":/etc/gordon/role.toml:ro", volumeName + ":/var/lib/gordon:U,unknown"},
		"conflicting duplicate":                  {configPath + ":/etc/gordon/role.toml:ro", volumeName + ":/var/lib/gordon:U", volumeName + ":/var/lib/gordon:rw"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				require.Equal(t, "/v1.41/containers/existing/json", request.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"Id": "existing", "Name": "/" + command.TargetComponentID, "Image": "sha256:fixture", "Created": "2026-05-05T00:00:00Z",
					"Config":     map[string]any{"Image": "gordon:fixture", "User": identity.User, "Labels": componentLifecycleLabels(command)},
					"HostConfig": map[string]any{"Binds": binds, "UsernsMode": componentKeepIDMode(identity), "CapDrop": []string{"ALL"}, "CapAdd": []string{}, "SecurityOpt": []string{"no-new-privileges:true"}},
					"State":      map[string]any{"Status": "running", "ExitCode": 0},
					"Mounts": []map[string]any{
						{"Type": "bind", "Source": configPath, "Destination": "/etc/gordon/role.toml", "Mode": "ro", "RW": false},
						{"Type": "volume", "Name": volumeName, "Source": "/home/fixture/.local/share/containers/storage/volumes/" + volumeName + "/_data", "Destination": "/var/lib/gordon", "Mode": "", "RW": true},
					},
					"NetworkSettings": map[string]any{"Ports": map[string]any{}},
				}))
			}))
			defer server.Close()

			host := strings.TrimPrefix(server.URL, "http://")
			apiClient, err := client.NewClientWithOpts(client.WithHost("tcp://"+host), client.WithVersion("1.41"), client.WithHTTPClient(server.Client()))
			require.NoError(t, err)
			inspected, err := dockeradapter.NewRuntimeWithClient(apiClient).InspectContainer(t.Context(), "existing")
			require.NoError(t, err)
			manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce}}
			require.ErrorIs(t, manager.validateExistingLifecycleMounts(inspected, command), ErrRuntimePolicyDenied)
		})
	}
}

func cloneVolumeOptions(options map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(options))
	for destination, values := range options {
		cloned[destination] = append([]string(nil), values...)
	}
	return cloned
}

func TestRuntimeComponentLifecycleRejectsCrossRoleGenerationVolume(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "runtime.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[runtime]\n"), 0o600))
	command := managedSecretsLifecycleCommand(domain.ComponentRoleRuntime, domain.RuntimeComponentLifecycleHealth, configPath)
	identity, _ := domain.FixedComponentProcessIdentity(domain.ComponentRoleRuntime)
	container := &domain.Container{
		ID: "existing", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command), User: identity.User,
		UsernsMode: componentKeepIDMode(identity), CapDrop: []string{"ALL"}, NoNewPrivileges: true,
		VolumeMounts: []domain.ContainerVolumeMount{
			{Type: "bind", Source: configPath, Destination: "/etc/gordon/role.toml", ReadOnly: true},
			{Type: "volume", Name: "gordon-control-fixture-g1", Destination: "/var/lib/gordon", Options: []string{domain.ContainerVolumeOptionChown}},
		},
	}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, container.ID).Return(container, nil).Once()
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	require.ErrorIs(t, applyTestComponentLifecycle(manager, t.Context(), command), ErrRuntimePolicyDenied)
}

func TestRuntimeComponentLifecycleReadRejectsExistingContainerWithoutDesiredIdentity(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	configPath := filepath.Join(configDir, "edge.toml")
	require.NoError(t, os.WriteFile(configPath, []byte("[edge]\n"), 0o600))
	command := managedSecretsLifecycleCommand(domain.ComponentRoleEdge, domain.RuntimeComponentLifecycleHealth, configPath)
	identity, _ := domain.FixedComponentProcessIdentity(domain.ComponentRoleEdge)
	container := &domain.Container{
		ID: "existing", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command), User: identity.User,
		UsernsMode: componentKeepIDMode(identity), CapDrop: []string{"ALL"}, NoNewPrivileges: true,
		VolumeMounts: []domain.ContainerVolumeMount{{Type: "bind", Source: configPath, Destination: "/etc/gordon/role.toml", ReadOnly: true}},
	}
	delete(container.Labels, domain.LabelComponentDesiredStateHash)
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, container.ID).Return(container, nil).Once()
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	require.ErrorIs(t, applyTestComponentLifecycle(manager, t.Context(), command), ErrRuntimePolicyDenied)
}

func TestRuntimeComponentLifecycleRejectsUnlabeledGordonNamedWorkload(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	workload := &domain.Container{ID: "workload", Name: "gordon-edge-fixture-g1", Labels: map[string]string{domain.LabelDomain: "app.example"}}
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{workload}, nil).Once()
	runtime.EXPECT().InspectContainer(mock.Anything, workload.ID).Return(workload, nil).Once()
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	err := applyTestComponentLifecycle(manager, context.Background(), domain.RuntimeSelfUpdateCommand{
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
	err := applyTestComponentLifecycle(manager, context.Background(), domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "test", IdempotencyKey: "test", Generation: 2, SourceComponentID: "gordon-control"}, TargetComponentID: "gordon-edge-fixture-g2", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g2", ConfigFile: configPath, PortPublishes: []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8081, Protocol: domain.NetworkProtocolTCP}}, PreserveVolumes: true,
	})
	require.NoError(t, err)
}

func TestRuntimeComponentSocketMountAcceptsUnixSocketNameWithoutSockSuffix(t *testing.T) {
	const sourcePath = "/run/user/1000/podman/native-api"
	source, env, err := runtimeComponentSocketMount([]string{"PODMAN_HOST=unix://" + sourcePath, "SAFE=value"})
	require.NoError(t, err)
	require.Equal(t, sourcePath, source)
	require.Equal(t, []string{"SAFE=value", "DOCKER_HOST=unix:///run/gordon/runtime.sock"}, env)
}

func TestRuntimeComponentLifecycleMountsExactSelectedSocketAndValidatesExistingContainer(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "migration", "config", "fixture", "1")
	envDir := filepath.Join(root, "migration", "env", "fixture", "1")
	require.NoError(t, os.MkdirAll(configDir, 0o700))
	require.NoError(t, os.MkdirAll(envDir, 0o700))
	configPath := filepath.Join(configDir, "runtime.toml")
	envPath := filepath.Join(envDir, "runtime.env")
	const sourcePath = "/run/user/1000/podman/native-api"
	require.NoError(t, os.WriteFile(configPath, []byte("[runtime]\n"), 0o600))
	require.NoError(t, os.WriteFile(envPath, []byte("DOCKER_HOST=unix://"+sourcePath+"\n"), 0o600))

	manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce}}
	command := managedSecretsLifecycleCommand(domain.ComponentRoleRuntime, domain.RuntimeComponentLifecycleStart, configPath)
	command.EnvironmentFile = envPath
	config, err := manager.componentConfig(command, nil)
	require.NoError(t, err)
	assert.Equal(t, sourcePath, config.ReadOnlyVolumes["/run/gordon/runtime.sock"])
	assert.Contains(t, config.Env, "DOCKER_HOST=unix:///run/gordon/runtime.sock")
	assert.NotContains(t, config.Env, "DOCKER_HOST=unix://"+sourcePath)

	identity, _ := domain.FixedComponentProcessIdentity(domain.ComponentRoleRuntime)
	existing := &domain.Container{
		ID: "existing", Name: command.TargetComponentID, Labels: componentLifecycleLabels(command), User: identity.User,
		UsernsMode: componentKeepIDMode(identity), CapDrop: []string{"ALL"}, NoNewPrivileges: true,
	}
	for destination, source := range config.Volumes {
		existing.VolumeMounts = append(existing.VolumeMounts, domain.ContainerVolumeMount{Type: "volume", Name: source, Destination: destination, Options: config.VolumeOptions[destination]})
	}
	for destination, source := range config.ReadOnlyVolumes {
		existing.VolumeMounts = append(existing.VolumeMounts, domain.ContainerVolumeMount{Type: "bind", Source: source, Destination: destination, ReadOnly: true})
	}
	require.NoError(t, manager.validateExistingLifecycleMounts(existing, command))
	for index := range existing.VolumeMounts {
		if existing.VolumeMounts[index].Destination == "/run/gordon/runtime.sock" {
			existing.VolumeMounts[index].Source = "/private/other/podman.sock"
		}
	}
	require.ErrorIs(t, manager.validateExistingLifecycleMounts(existing, command), ErrRuntimePolicyDenied)
}

func TestRuntimeComponentSocketMountFailsClosedWithoutEndpointLeakage(t *testing.T) {
	for name, environment := range map[string][]string{
		"absent":      {"SAFE=value"},
		"remote":      {"DOCKER_HOST=ssh://private.example/run/podman.sock"},
		"conflicting": {"DOCKER_HOST=unix:///run/user/1000/podman/podman.sock", "PODMAN_HOST=unix:///private/conflict/podman.sock"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := runtimeComponentSocketMount(environment)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private.example")
			assert.NotContains(t, err.Error(), "private/conflict")
		})
	}
}

func TestPrivateLifecycleEnvironmentOpenAnchorsDescriptorAcrossPathReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.env")
	const original = "DOCKER_HOST=unix:///run/user/1000/podman/native-api\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))

	file, err := openPrivateComponentEnvironmentFile(path, "")
	require.NoError(t, err)
	defer file.Close()
	require.NoError(t, os.Rename(path, path+".opened"))
	require.NoError(t, os.WriteFile(path, []byte("DOCKER_HOST=unix:///replacement\n"), 0o600))

	data, err := io.ReadAll(file)
	require.NoError(t, err)
	assert.Equal(t, original, string(data))
}

func TestComponentLifecycleEnvironmentRejectsSymlinkAndOversizedDescriptor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "migration")
	directory := filepath.Join(root, "env", "fixture", "1")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, "runtime.env")
	target := filepath.Join(t.TempDir(), "target.env")
	require.NoError(t, os.WriteFile(target, []byte("DOCKER_HOST=unix:///run/user/1000/podman/native-api\n"), 0o600))
	require.NoError(t, os.Symlink(target, path))
	command := managedSecretsLifecycleCommand(domain.ComponentRoleRuntime, domain.RuntimeComponentLifecycleStart, filepath.Join(root, "config", "fixture", "1", "runtime.toml"))
	_, err := componentLifecycleEnvironment(command, path, root)
	require.Error(t, err)

	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, make([]byte, maxComponentLifecycleEnvironmentBytes+1), 0o600))
	_, err = componentLifecycleEnvironment(command, path, root)
	require.Error(t, err)

	require.NoError(t, os.RemoveAll(filepath.Join(root, "env", "fixture")))
	outside := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, os.MkdirAll(filepath.Join(outside, "1"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "1", "runtime.env"), []byte("DOCKER_HOST=unix:///replacement\n"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "env", "fixture")))
	_, err = componentLifecycleEnvironment(command, path, root)
	require.Error(t, err)
}

func TestRuntimeComponentLifecycleAnchorsGeneratedFilesUnderConfiguredMigrationRoot(t *testing.T) {
	configuredRoot := filepath.Join(t.TempDir(), "migration")
	foreignRoot := filepath.Join(t.TempDir(), "migration")
	for _, root := range []string{configuredRoot, foreignRoot} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "config", "fixture", "1"), 0o700))
		require.NoError(t, os.MkdirAll(filepath.Join(root, "env", "fixture", "1"), 0o700))
	}
	configuredConfig := filepath.Join(configuredRoot, "config", "fixture", "1", "runtime.toml")
	foreignConfig := filepath.Join(foreignRoot, "config", "fixture", "1", "runtime.toml")
	configuredEnv := filepath.Join(configuredRoot, "env", "fixture", "1", "runtime.env")
	foreignEnv := filepath.Join(foreignRoot, "env", "fixture", "1", "runtime.env")
	for _, path := range []string{configuredConfig, foreignConfig} {
		require.NoError(t, os.WriteFile(path, []byte("[runtime]\n"), 0o600))
	}
	for _, path := range []string{configuredEnv, foreignEnv} {
		require.NoError(t, os.WriteFile(path, []byte("DOCKER_HOST=unix:///run/user/1000/podman/native-api\n"), 0o600))
	}
	manager := &runtimeComponentLifecycleManager{policy: RuntimePolicy{Mode: RuntimePolicyModeEnforce, MigrationStateRoot: configuredRoot}}

	command := managedSecretsLifecycleCommand(domain.ComponentRoleRuntime, domain.RuntimeComponentLifecycleStart, foreignConfig)
	command.EnvironmentFile = configuredEnv
	_, err := manager.componentConfig(command, nil)
	require.Error(t, err)

	command.ConfigFile = configuredConfig
	command.EnvironmentFile = foreignEnv
	_, err = manager.componentConfig(command, nil)
	require.Error(t, err)
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
	err := applyTestComponentLifecycle(manager, context.Background(), domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "test", IdempotencyKey: "test", Generation: 2, SourceComponentID: "gordon-control"}, TargetComponentID: "gordon-edge-fixture-g2", TargetComponentRole: domain.ComponentRoleEdge,
		TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleStart, DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g2", PreserveVolumes: true,
	})
	require.Error(t, err)
}
