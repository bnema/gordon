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
	commands       []domain.RuntimeSelfUpdateCommand
	subphases      []domain.MigrationCutoverSubphase
	failureRetries []bool
	err            error
}

func (c *recordingMigrationCutoverCommitter) RecordMigrationCutoverSubphase(_ context.Context, _ domain.RuntimeSelfUpdateCommand, subphase domain.MigrationCutoverSubphase) error {
	c.subphases = append(c.subphases, subphase)
	return nil
}

func (c *recordingMigrationCutoverCommitter) CommitMigrationCutover(_ context.Context, command domain.RuntimeSelfUpdateCommand) error {
	c.commands = append(c.commands, command)
	return c.err
}

func (c *recordingMigrationCutoverCommitter) RecordMigrationCutoverFailure(_ context.Context, _ domain.RuntimeSelfUpdateCommand, _ string, retryable bool) error {
	c.failureRetries = append(c.failureRetries, retryable)
	return nil
}

type recoveringMigrationCutoverCommitter struct {
	recordingMigrationCutoverCommitter
	subphase domain.MigrationCutoverSubphase
}

func (c *recoveringMigrationCutoverCommitter) MigrationCutoverSubphase(_ context.Context, _ domain.RuntimeSelfUpdateCommand) (domain.MigrationCutoverSubphase, error) {
	return c.subphase, nil
}

func TestRuntimeComponentLifecycleActivateColdCutoverWithoutOldContainer(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()

	committer := &recordingMigrationCutoverCommitter{}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config)))
	require.Len(t, committer.commands, 1)
	assert.NotContains(t, committer.subphases, domain.MigrationCutoverSubphaseBeforeOldStop)
}

func TestRuntimeComponentLifecycleActivateColdFailuresRestoreAndProvePreparedEdge(t *testing.T) {
	for _, failure := range []string{"stop-prepared", "remove-prepared", "create-final", "start-final", "health-final", "commit-final"} {
		t.Run(failure, func(t *testing.T) {
			config := cutoverConfig(t)
			prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Ports: []int{18080}, Labels: componentLabels("edge")})
			restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: prepared.Name, Ports: []int{18080}, Labels: componentLabels("edge")})
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
			runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Twice()
			runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
			runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()

			proof := prepared
			if failure == "stop-prepared" {
				runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(errors.New("injected stop failure")).Once()
			} else {
				runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
				if failure == "remove-prepared" {
					runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(errors.New("injected remove failure")).Once()
					runtime.EXPECT().StartContainer(mock.Anything, "prepared").Return(nil).Once()
				} else {
					runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
					if failure == "create-final" {
						runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(nil, errors.New("injected create failure")).Once()
					} else {
						runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
						if failure == "start-final" {
							runtime.EXPECT().StartContainer(mock.Anything, "final").Return(errors.New("injected start failure")).Once()
							runtime.EXPECT().StopContainer(mock.Anything, "final").Return(nil).Once()
							runtime.EXPECT().RemoveContainer(mock.Anything, "final", true).Return(nil).Once()
						} else {
							runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
							runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(failure != "health-final", nil).Once()
							if failure == "commit-final" {
								runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()
							}
							runtime.EXPECT().StopContainer(mock.Anything, "final").Return(nil).Once()
							runtime.EXPECT().RemoveContainer(mock.Anything, "final", true).Return(nil).Once()
						}
					}
					runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
					runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
					proof = restored
				}
			}
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{proof}, nil).Once()
			runtime.EXPECT().IsContainerRunning(mock.Anything, proof.ID).Return(true, nil).Once()
			runtime.EXPECT().GetContainerHealthStatus(mock.Anything, proof.ID).Return("healthy", true, nil).Once()

			committer := &recordingMigrationCutoverCommitter{}
			if failure == "commit-final" {
				committer.err = errors.New("injected commit failure")
			}
			manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
			err := applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config))
			require.Error(t, err)
			assert.Equal(t, []bool{true}, committer.failureRetries)
		})
	}
}

func TestRuntimeComponentLifecycleActivateColdNetworkFailureRestoresAndProvesPreparedEdge(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Ports: []int{18080}, Labels: componentLabels("edge")})
	restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: prepared.Name, Ports: []int{18080}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
	runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Twice()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test", prepared.Name}}}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, prepared.Name, "gordon-app-fixture").Return(errors.New("injected network failure")).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "final", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, prepared.Name, "gordon-app-fixture").Return(nil).Twice()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{restored}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "restored").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "restored").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test", restored.Name}}}, nil).Once()

	command := cutoverCommand(config)
	command.EdgeAppNetworks = []string{"gordon-app-fixture"}
	committer := &recordingMigrationCutoverCommitter{}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"}), committer)
	require.Error(t, applyTestComponentLifecycle(manager, context.Background(), command))
	assert.Equal(t, []bool{true}, committer.failureRetries)
}

func TestRuntimeComponentLifecycleActivateColdRecoveryCommitsHealthyFinal(t *testing.T) {
	config := cutoverConfig(t)
	final := edgeLifecycleFixture(config, &domain.Container{ID: "final", Name: "gordon-edge-fixture-g1", Ports: []int{8080, 5000}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, final.ID).Return(final, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{final}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()
	committer := &recoveringMigrationCutoverCommitter{subphase: domain.MigrationCutoverSubphaseBeforeCommit}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config)))
	require.Len(t, committer.commands, 1)
}

func TestRuntimeComponentLifecycleActivateColdRecoveryRestoresPreparedEdge(t *testing.T) {
	config := cutoverConfig(t)
	restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: "gordon-edge-fixture-g1", Ports: []int{18080}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return(nil, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{restored}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "restored").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "restored").Return("healthy", true, nil).Once()
	committer := &recoveringMigrationCutoverCommitter{subphase: domain.MigrationCutoverSubphaseBeforePreparedRemove}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
	err := applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config))
	require.Error(t, err)
	assert.Equal(t, []bool{true}, committer.failureRetries)
}

func TestRuntimeComponentLifecycleActivateTransfersManagedListenerTransactionally(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
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
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(config))))
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, MigrationStateRoot: root}), committer)
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config)))
	require.Len(t, committer.commands, 1)
	assert.Equal(t, domain.RuntimeComponentLifecycleActivate, committer.commands[0].LifecycleAction)
}

func TestRuntimeComponentLifecycleActivateRecordsDurableIntentBeforeEachMutation(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
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
	committer := &recordingMigrationCutoverCommitter{}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config)))
	assert.Equal(t, []domain.MigrationCutoverSubphase{
		domain.MigrationCutoverSubphaseBeforeOldStop,
		domain.MigrationCutoverSubphaseBeforePreparedStop,
		domain.MigrationCutoverSubphaseBeforePreparedRemove,
		domain.MigrationCutoverSubphaseBeforeFinalCreate,
		domain.MigrationCutoverSubphaseBeforeFinalStart,
		domain.MigrationCutoverSubphaseBeforeCommit,
	}, committer.subphases)
}

func TestRuntimeComponentLifecycleActivateRecoversWhenPreparedEdgeWasRemoved(t *testing.T) {
	config := cutoverConfig(t)
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: "gordon-edge-fixture-g1", Ports: []int{18080}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{old}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "old").Return(false, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{old, restored}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "old").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "old").Return("healthy", true, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "restored").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "restored").Return("healthy", true, nil).Once()
	committer := &recoveringMigrationCutoverCommitter{subphase: domain.MigrationCutoverSubphaseBeforePreparedRemove}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
	err := applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config))
	require.Error(t, err)
	assert.Equal(t, []bool{true}, committer.failureRetries, "retryability requires listener and health proof")
	assert.NotContains(t, err.Error(), "old-monolith")
}

func TestRuntimeComponentLifecycleActivateCompletesAfterCallerCancellation(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, old}, nil).Once()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.EXPECT().StopContainer(mock.Anything, "old").Run(func(context.Context, string) { cancel() }).Return(nil).Once()
	uncanceled := mock.MatchedBy(func(value context.Context) bool { return value.Err() == nil })
	runtime.EXPECT().StopContainer(uncanceled, "prepared").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(uncanceled, "prepared", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(uncanceled, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
	runtime.EXPECT().StartContainer(uncanceled, "final").Return(nil).Once()
	runtime.EXPECT().IsContainerRunning(uncanceled, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(uncanceled, "final").Return("healthy", true, nil).Once()

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	require.NoError(t, applyTestComponentLifecycle(manager, ctx, cutoverCommand(config)))
}

func TestRuntimeComponentLifecycleActivateRetriesTransientFinalListenerRelease(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, old}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "old").Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(nil, errors.New("listen tcp 0.0.0.0:8080: bind: address already in use")).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()

	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config)))
}

func TestRuntimeComponentLifecycleActivatePreservesManagedAppNetwork(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
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
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, "gordon-edge-fixture-g1", "gordon-app-fixture").Return(nil).Twice()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test", prepared.Name}}}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()

	command := cutoverCommand(config)
	command.EdgeAppNetworks = []string{"gordon-app-fixture"}
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), command))
}

func TestRuntimeComponentLifecycleActivateRollsBackWhenAppNetworkRestoreFails(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: prepared.Name, Ports: []int{18080}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
	runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
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
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, restored.Name, "gordon-app-fixture").Return(nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
	expectEdgeRollbackInventoryProof(runtime, old, restored, []string{"gordon-app-fixture"})

	command := cutoverCommand(config)
	command.EdgeAppNetworks = []string{"gordon-app-fixture"}
	manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"})
	err := applyTestComponentLifecycle(manager, context.Background(), command)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "injected network failure", "raw runtime errors must not cross the lifecycle boundary")
}

func TestRuntimeComponentLifecycleActivateRollsBackWhenDurableCutoverCommitFails(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: prepared.Name, Ports: []int{18080}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
	runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
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
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
	expectRollbackInventoryProofWithoutNetworks(runtime, old, restored)

	committer := &recordingMigrationCutoverCommitter{err: errors.New("injected durable write failure")}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
	err := applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config))
	require.Error(t, err)
	require.Len(t, committer.commands, 1)
	assert.Equal(t, []bool{true}, committer.failureRetries, "retryability is recorded only after every compensation succeeds")
	assert.NotContains(t, err.Error(), "injected durable write failure", "runtime errors must remain local")
}

func TestRuntimeComponentLifecycleActivateMarksRollbackNonretryableWhenRestoreFails(t *testing.T) {
	config := cutoverConfig(t)
	prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Ports: []int{18080}, Labels: componentLabels("edge")})
	old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, old}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "old").Return(nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(errors.New("injected prepared stop failure")).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "old").Return(errors.New("injected old restore failure")).Once()
	expectRollbackInventoryProofWithoutNetworks(runtime, old, prepared)

	committer := &recordingMigrationCutoverCommitter{}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce}), committer)
	err := applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config))
	require.Error(t, err)
	assert.Equal(t, []bool{false}, committer.failureRetries)
	assert.NotContains(t, err.Error(), "injected old restore failure")
}

func TestRuntimeComponentLifecycleActivateRollsBackEveryMutationFailure(t *testing.T) {
	for _, failure := range []string{"stop-old", "stop-prepared", "remove-prepared", "create-final", "start-final", "postcheck-final"} {
		t.Run(failure, func(t *testing.T) {
			config := cutoverConfig(t)
			prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Ports: []int{18080}, Labels: componentLabels("edge")})
			old := &domain.Container{ID: "old", Name: "old-monolith", Ports: []int{8080, 5000}, Labels: map[string]string{domain.LabelManaged: "true"}}
			restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: prepared.Name, Ports: []int{18080}, Labels: componentLabels("edge")})
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
			runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
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
					expectRollbackInventoryProofWithoutNetworks(runtime, old, prepared)
				} else {
					runtime.EXPECT().StopContainer(mock.Anything, "prepared").Return(nil).Once()
					if failure == "remove-prepared" {
						runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(errors.New("injected remove failure")).Once()
						runtime.EXPECT().StartContainer(mock.Anything, "prepared").Return(nil).Once()
						runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
						expectRollbackInventoryProofWithoutNetworks(runtime, old, prepared)
					} else {
						runtime.EXPECT().RemoveContainer(mock.Anything, "prepared", true).Return(nil).Once()
						if failure == "create-final" {
							runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(nil, errors.New("injected create failure")).Once()
							runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
							runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
							runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
							expectRollbackInventoryProofWithoutNetworks(runtime, old, restored)
						} else {
							runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(&domain.Container{ID: "final"}, nil).Once()
							if failure == "start-final" {
								runtime.EXPECT().StartContainer(mock.Anything, "final").Return(errors.New("injected start failure")).Once()
							} else {
								runtime.EXPECT().StartContainer(mock.Anything, "final").Return(nil).Once()
								runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(false, nil).Once()
							}
							runtime.EXPECT().StopContainer(mock.Anything, "final").Return(nil).Once()
							runtime.EXPECT().RemoveContainer(mock.Anything, "final", true).Return(nil).Once()
							runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
							runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
							runtime.EXPECT().StartContainer(mock.Anything, "old").Return(nil).Once()
							expectRollbackInventoryProofWithoutNetworks(runtime, old, restored)
						}
					}
				}
			}
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
			err := applyTestComponentLifecycle(manager, context.Background(), cutoverCommand(config))
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
			prepared := edgeLifecycleFixture(config, &domain.Container{ID: "prepared", Name: "gordon-edge-fixture-g1", Labels: componentLabels("edge")})
			runtime := outmocks.NewMockContainerRuntime(t)
			runtime.EXPECT().InspectContainer(mock.Anything, prepared.ID).Return(prepared, nil).Maybe()
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared}, nil).Once()
			runtime.EXPECT().IsContainerRunning(mock.Anything, "prepared").Return(true, nil).Once()
			runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "prepared").Return("healthy", true, nil).Once()
			runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{prepared, test.old}, nil).Once()
			command := cutoverCommand(config)
			command.FinalPortPublishes = test.ports
			manager := NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce})
			require.Error(t, applyTestComponentLifecycle(manager, context.Background(), command))
		})
	}
}

func TestCompletedFinalCutoverRequiresAllEdgeAppNetworks(t *testing.T) {
	config := cutoverConfig(t)
	final := edgeLifecycleFixture(config, &domain.Container{ID: "final", Name: "gordon-edge-fixture-g1", Ports: []int{8080, 5000}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, final.ID).Return(final, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{final}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, final.Name, "gordon-app-fixture").Return(nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test", final.Name}}}, nil).Once()
	committer := &recoveringMigrationCutoverCommitter{subphase: domain.MigrationCutoverSubphaseBeforeCommit}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"}), committer)
	command := cutoverCommand(config)
	command.EdgeAppNetworks = []string{"gordon-app-fixture"}
	require.NoError(t, applyTestComponentLifecycle(manager, context.Background(), command))
	require.Len(t, committer.commands, 1)
}

func TestCompletedFinalCutoverRejectsMissingEdgeAppNetworks(t *testing.T) {
	config := cutoverConfig(t)
	final := edgeLifecycleFixture(config, &domain.Container{ID: "final", Name: "gordon-edge-fixture-g1", Ports: []int{8080, 5000}, Labels: componentLabels("edge")})
	restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: "gordon-edge-fixture-g1", Ports: []int{18080}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, final.ID).Return(final, nil).Maybe()
	runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{final}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "final").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "final").Return("healthy", true, nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, final.Name, "gordon-app-fixture").Return(nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test"}}}, nil).Once()
	runtime.EXPECT().StopContainer(mock.Anything, "final").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "final", true).Return(nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, restored.Name, "gordon-app-fixture").Return(nil).Once()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{restored}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "restored").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "restored").Return("healthy", true, nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, restored.Name, "gordon-app-fixture").Return(nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test", restored.Name}}}, nil).Once()
	committer := &recoveringMigrationCutoverCommitter{subphase: domain.MigrationCutoverSubphaseBeforeCommit}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"}), committer)
	command := cutoverCommand(config)
	command.EdgeAppNetworks = []string{"gordon-app-fixture"}
	err := applyTestComponentLifecycle(manager, context.Background(), command)
	require.Error(t, err)
	assert.Empty(t, committer.commands)
	assert.Equal(t, []bool{true}, committer.failureRetries)
}

func TestRestorePreparedEdgeReattachesAppNetworks(t *testing.T) {
	config := cutoverConfig(t)
	restored := edgeLifecycleFixture(config, &domain.Container{ID: "restored", Name: "gordon-edge-fixture-g1", Ports: []int{18080}, Labels: componentLabels("edge")})
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.EXPECT().InspectContainer(mock.Anything, restored.ID).Return(restored, nil).Maybe()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return(nil, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(restored, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "restored").Return(nil).Once()
	runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, restored.Name, "gordon-app-fixture").Return(nil).Twice()
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{restored}, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "restored").Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, "restored").Return("healthy", true, nil).Once()
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{{Name: "gordon-app-fixture", Labels: map[string]string{domain.LabelManaged: "true"}, Containers: []string{"gordon-target-app-example-test", restored.Name}}}, nil).Once()
	committer := &recoveringMigrationCutoverCommitter{subphase: domain.MigrationCutoverSubphaseBeforePreparedRemove}
	manager := WithMigrationCutoverCommitter(NewRuntimeComponentLifecycleManager(runtime, RuntimePolicy{Mode: RuntimePolicyModeEnforce, ManagedNetworkPrefix: "gordon"}), committer)
	command := cutoverCommand(config)
	command.EdgeAppNetworks = []string{"gordon-app-fixture"}
	err := applyTestComponentLifecycle(manager, context.Background(), command)
	require.Error(t, err)
	assert.Equal(t, []bool{true}, committer.failureRetries)
}

func expectRollbackInventoryProofWithoutNetworks(runtime *outmocks.MockContainerRuntime, old, prepared *domain.Container) {
	containers := []*domain.Container{prepared}
	if old != nil {
		containers = append(containers, old)
	}
	runtime.EXPECT().ListContainers(mock.Anything, true).Return(containers, nil).Once()
	if old != nil {
		runtime.EXPECT().IsContainerRunning(mock.Anything, old.ID).Return(true, nil).Once()
		runtime.EXPECT().GetContainerHealthStatus(mock.Anything, old.ID).Return("healthy", true, nil).Once()
	}
	runtime.EXPECT().IsContainerRunning(mock.Anything, prepared.ID).Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, prepared.ID).Return("healthy", true, nil).Once()
}

func expectEdgeRollbackInventoryProof(runtime *outmocks.MockContainerRuntime, old, restored *domain.Container, networkNames []string) {
	containers := []*domain.Container{restored}
	if old != nil {
		containers = append(containers, old)
	}
	runtime.EXPECT().ListContainers(mock.Anything, true).Return(containers, nil).Once()
	for _, name := range networkNames {
		runtime.EXPECT().ConnectContainerToNetwork(mock.Anything, restored.Name, name).Return(nil).Once()
	}
	networks := make([]*domain.NetworkInfo, 0, len(networkNames))
	for _, name := range networkNames {
		networks = append(networks, &domain.NetworkInfo{
			Name:       name,
			Labels:     map[string]string{domain.LabelManaged: "true"},
			Containers: []string{"gordon-target-app-example-test", restored.Name},
		})
	}
	runtime.EXPECT().ListNetworks(mock.Anything).Return(networks, nil).Once()
	if old != nil {
		runtime.EXPECT().IsContainerRunning(mock.Anything, old.ID).Return(true, nil).Once()
		runtime.EXPECT().GetContainerHealthStatus(mock.Anything, old.ID).Return("healthy", true, nil).Once()
	}
	runtime.EXPECT().IsContainerRunning(mock.Anything, restored.ID).Return(true, nil).Once()
	runtime.EXPECT().GetContainerHealthStatus(mock.Anything, restored.ID).Return("healthy", true, nil).Once()
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

func TestRuntimeComponentLifecycleRegistryReusesCanonicalStorage(t *testing.T) {
	storage := filepath.Join(t.TempDir(), "registry")
	require.NoError(t, os.Mkdir(storage, 0o700))
	config := cutoverConfig(t)
	command := cutoverCommand(config)
	command.LifecycleAction = domain.RuntimeComponentLifecycleStart
	command.TargetComponentRole = domain.ComponentRoleRegistry
	command.LifecycleProfile, _ = domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleRegistry)
	command.TargetComponentID = "gordon-registry-fixture-g1"
	command.ConfigFile = filepath.Join(filepath.Dir(config), "registry.toml")
	require.NoError(t, os.WriteFile(command.ConfigFile, []byte("[registry]\n"), 0o600))

	manager := NewRuntimeComponentLifecycleManager(outmocks.NewMockContainerRuntime(t), RuntimePolicy{Mode: RuntimePolicyModeEnforce, RegistryStorageRoot: storage}).(*runtimeComponentLifecycleManager)
	component, err := manager.componentConfig(command, nil)
	require.NoError(t, err)
	assert.Equal(t, storage, component.Volumes["/var/lib/gordon/registry"])
	assert.Len(t, component.Volumes, 1, "registry must not receive an empty generation volume")
	assert.Empty(t, component.VolumeOptions, "canonical registry storage is a host bind and must never receive U")
}

func edgeLifecycleFixture(config string, container *domain.Container) *domain.Container {
	if len(container.Ports) == 2 {
		config = filepath.Join(filepath.Dir(config), "edge-final.toml")
	}
	container.User = "21003:21003"
	container.UsernsMode = "keep-id:uid=21003,gid=21003"
	container.CapDrop = []string{"ALL"}
	container.NoNewPrivileges = true
	container.VolumeMounts = []domain.ContainerVolumeMount{{Type: "bind", Source: config, Destination: "/etc/gordon/role.toml", ReadOnly: true}}
	return container
}

func componentLabels(role string) map[string]string {
	return map[string]string{domain.LabelComponent: "true", domain.LabelComponentRole: role, domain.LabelComponentGeneration: "1", domain.LabelComponentMigrationID: "fixture", domain.LabelComponentOwner: "runtime", domain.LabelComponentDesiredStateHash: "fixture"}
}

func cutoverCommand(config string) domain.RuntimeSelfUpdateCommand {
	profile, _ := domain.FixedRuntimeComponentLifecycleProfile(domain.ComponentRoleEdge)
	return domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "cutover", IdempotencyKey: "cutover", Generation: 1, SourceComponentID: "gordon-control"},
		TargetComponentID:      "gordon-edge-fixture-g1", TargetComponentRole: domain.ComponentRoleEdge, TargetVersion: "v2", Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:fixture", LifecycleAction: domain.RuntimeComponentLifecycleActivate, LifecycleProfile: profile,
		DesiredImage: "example.invalid/gordon:v2", DesiredStateHash: "fixture", InternalNetwork: "gordon-internal-fixture-g1", ConfigFile: config, OldServingComponentID: "old-monolith", PreserveVolumes: true,
		PortPublishes:      []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}},
		FinalPortPublishes: []domain.ContainerPortPublish{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 8080, Protocol: domain.NetworkProtocolTCP}, {HostIP: "0.0.0.0", HostPort: 5000, ContainerPort: 5000, Protocol: domain.NetworkProtocolTCP}},
	}
}
