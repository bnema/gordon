package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

type recordingMigrationCutoverCommitter struct {
	commands []domain.RuntimeSelfUpdateCommand
	err      error
}

func (c *recordingMigrationCutoverCommitter) CommitMigrationCutover(_ context.Context, command domain.RuntimeSelfUpdateCommand) error {
	c.commands = append(c.commands, command)
	return c.err
}

func TestRuntimeComponentLifecycleActivateTransfersManagedListenerTransactionally(t *testing.T) {
	config := cutoverConfig(t)
	prepared := &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")}
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, old}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "old").Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(c *domain.ContainerConfig) bool {
		return c.Name == prepared.Name && len(c.PortPublishes) == 2 && c.PortPublishes[0].HostPort == 8080 && c.PortPublishes[1].HostPort == 5000 && filepath.Base(c.ReadOnlyVolumes["/etc/gordon/role.toml"]) == "edge-final.toml"
	})).Return(&domain.Container{ID: "final"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()

	committer := &recordingMigrationCutoverCommitter{}
	root := filepath.Dir(filepath.Dir(filepath.Dir(config)))
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, MigrationStateRoot: root}), committer)
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), cutoverCommand(config)))
	require.Len(t, committer.commands, 1)
	assert.Equal(t, domain.RuntimeComponentLifecycleActivate, committer.commands[0].LifecycleAction)
}

func TestRuntimeComponentLifecycleActivatePreservesManagedAppNetwork(t *testing.T) {
	config := cutoverConfig(t)
	prepared := &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")}
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, old}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test", prepared.Name}}}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "old").Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, "gordon-edge-fixture-g1", "gordon-app-fixture").Return(nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()

	command := cutoverCommand(config)
	command.EdgeAppNetworks = []string{"gordon-app-fixture"}
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	require.NoError(t, manager.ApplyComponentLifecycle(context.Background(), command))
}

func TestRuntimeComponentLifecycleActivateRollsBackWhenAppNetworkRestoreFails(t *testing.T) {
	config := cutoverConfig(t)
	prepared := &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")}
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, old}, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test", prepared.Name}}}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "old").Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, "gordon-edge-fixture-g1", "gordon-app-fixture").Return(errors.New("injected network failure")).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "final", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "restored"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()

	command := cutoverCommand(config)
	command.EdgeAppNetworks = []string{"gordon-app-fixture"}
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	err := manager.ApplyComponentLifecycle(context.Background(), command)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "injected network failure", "raw runtime errors must not cross the lifecycle boundary")
}

func TestRuntimeComponentLifecycleActivateRollsBackWhenDurableCutoverCommitFails(t *testing.T) {
	config := cutoverConfig(t)
	prepared := &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")}
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, old}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "old").Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "final", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "restored"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()

	committer := &recordingMigrationCutoverCommitter{err: errors.New("injected durable write failure")}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
	err := manager.ApplyComponentLifecycle(context.Background(), cutoverCommand(config))
	require.Error(t, err)
	require.Len(t, committer.commands, 1)
	assert.NotContains(t, err.Error(), "injected durable write failure", "runtime errors must remain local")
}

func TestRuntimeComponentLifecycleActivateRollsBackEveryMutationFailure(t *testing.T) {
	for _, failure := range []string{"stop-old", "stop-prepared", "remove-prepared", "create-final", "start-final", "postcheck-final"} {
		t.Run(failure, func(t *testing.T) {
			config := cutoverConfig(t)
			prepared := &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")}
			old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
			runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
			runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, old}, nil).Once()
			if failure == "stop-old" {
				runtime.EXPECT().StopContainer(mock.Anything, "old").Return(errors.New("injected stop failure")).Once()
			} else {
				runtime.EXPECT().StopContainer(mock.Anything, "old").Return(nil).Once()
				if failure == "stop-prepared" {
					runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(errors.New("injected stop failure")).Once()
					runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
				} else {
					runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
					if failure == "remove-prepared" {
						runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(errors.New("injected remove failure")).Once()
						runtime.EXPECT().StartContainer(mock.Anything, "prepared").Return(nil).Once()
						runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
					} else {
						runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
						if failure == "create-final" {
							runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(nil, errors.New("injected create failure")).Once()
						} else {
							runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
							if failure == "start-final" {
								runtime.EXPECT().StartContainer(mock.Anything, "final").Return(errors.New("injected start failure")).Once()
							} else {
								runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
								runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(false, nil).Once()
							}
						}
						if failure != "remove-prepared" {
							if failure != "create-final" {
								runtime.EXPECT().StopContainer(mock.Anything, "final").Return(nil).Once()
								runtime.EXPECT().RemoveContainer(mock.Anything, "final", true).Return(nil).Once()
							}
							runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "restored"}, nil).Once()
							runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
							runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
						}
					}
				}
			}
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
			err := manager.ApplyComponentLifecycle(context.Background(), cutoverCommand(config))
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "old-monolith", "errors never expose arbitrary engine data")
		})
	}
}

func TestRuntimeComponentLifecycleActivateRejectsUnmanagedOldOrFinalPorts(t *testing.T) {
	config := cutoverConfig(t)
	for _, test := range []struct {
		name  string
		old   *domain.Container
		ports []domain.ContainerPortPublish
	}{
		{name: "unmanaged old", old: &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}}, ports: cutoverCommand(config).FinalPortPublishes},
		{name: "unallowlisted final port", old: &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}, ports: []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 2375, ContainerPort: 2375, Protocol: domain.NetworkProtocolTCP}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prepared := &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")}
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
			runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
			runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, test.old}, nil).Once()
			command := cutoverCommand(config)
			command.FinalPortPublishes = test.ports
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
			require.Error(t, manager.ApplyComponentLifecycle(context.Background(), command))
		})
	}
}

func cutoverConfig(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "migration", "config", "fixture", "1")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	path := filepath.Join(directory, "edge.toml")
	require.NoError(t, os.WriteFile(path, []byte("[edge]\nmigration_probe_enabled = true\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "edge-final.toml"), []byte("[edge]\nmigration_probe_enabled = false\n"), 0o600))
	return path
}

func componentLabels(role string) map[string]string {
	return map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: role, domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture", domain.LabelComponentOwner: "runtime"}
}

func cutoverCommand(config string) domain.RuntimeSelfUpdateCommand {
	return domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "cutover", IdempotencyKey: "cutover", Generation: 1, SourceComponentID: "gordon-control"},
		TargetComponentID:      "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleActivate,
		DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: config, OldServingComponentID: "old-monolith", PreserveVolumes: true,
		PortPublishes:      []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}},
		FinalPortPublishes: []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}, {HostIP: "0.0.0.0", HostPort: 5000, ContainerPort: 5000, Protocol: domain.NetworkProtocolTCP}},
	}
}
