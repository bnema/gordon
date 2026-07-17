package services

import (
	"context"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestLocalRuntimeStandaloneServiceManagerApplyCreatesExactContainerConfig(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	command := applyStandaloneCommand(service, []string{"PUBLIC=value"}, "hash-1")
	var created *domain.ContainerConfig
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{}, nil).Once()
	runtime.On("InspectImageVolumes", mock.Anything, service.Image).Return([]string{"/data"}, nil).Once()
	runtime.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Run(func(args mock.Arguments) {
		created = args.Get(1).(*domain.ContainerConfig)
	}).Return(&domain.Container{ID: "created-1"}, nil).Once()
	runtime.On("StartContainer", mock.Anything, "created-1").Return(nil).Once()

	result, err := NewLocalRuntimeStandaloneServiceManager(runtime).ApplyStandaloneService(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	require.NotNil(t, created)
	assert.Equal(t, "game:latest", created.Image)
	assert.Equal(t, "gordon-service-game", created.Name)
	assert.Equal(t, []string{"PUBLIC=value"}, created.Env)
	assert.Equal(t, domain.RestartPolicyAlways, created.RestartPolicy)
	assert.Equal(t, map[string]string{
		domain.LabelManaged:                       "true",
		domain.LabelService:                       "true",
		domain.LabelServiceName:                   "game",
		domain.LabelServiceConfigHash:             "hash-1",
		domain.LabelServiceManagedVolumes:         "gordon-service-game-data",
		domain.LabelServiceCleanupPreserveVolumes: "true",
		domain.LabelServiceCleanupRemoveContainer: "true",
	}, created.Labels)
	assert.Equal(t, []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 38015, ContainerPort: 28015, Protocol: domain.NetworkProtocolUDP}}, created.PortPublishes)
	assert.Equal(t, map[string]string{"/data": "gordon-service-game-data"}, created.Volumes)
	assert.Nil(t, created.ReadOnlyVolumes)
}

func TestLocalRuntimeStandaloneServiceManagerApplyStartsStoppedAndCleansDuplicates(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	command := applyStandaloneCommand(service, []string{"PUBLIC=value"}, "hash-1")
	stopped := managedContainer("existing-1", service.Name, "hash-1", "exited")
	duplicate := managedContainer("existing-2", service.Name, "hash-1", "running")
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{stopped, duplicate}, nil).Once()
	runtime.On("RemoveContainer", mock.Anything, "existing-1", true).Return(nil).Once()

	result, err := NewLocalRuntimeStandaloneServiceManager(runtime).ApplyStandaloneService(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
}

func TestLocalRuntimeStandaloneServiceManagerApplyPrefersRunningDuplicateAndRecreatesStale(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	command := applyStandaloneCommand(service, []string{"PUBLIC=value"}, "new-hash")
	old := managedContainer("old-1", service.Name, "old-hash", "running")
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{old}, nil).Once()
	runtime.On("StopContainer", mock.Anything, "old-1").Return(nil).Once()
	runtime.On("RemoveContainer", mock.Anything, "old-1", true).Return(nil).Once()
	runtime.On("InspectImageVolumes", mock.Anything, service.Image).Return([]string{}, nil).Once()
	runtime.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{ID: "new-1"}, nil).Once()
	runtime.On("StartContainer", mock.Anything, "new-1").Return(nil).Once()

	result, err := NewLocalRuntimeStandaloneServiceManager(runtime).ApplyStandaloneService(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
}

func TestLocalRuntimeStandaloneServiceManagerApplyReadinessAndErrorAreSanitized(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	service.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/server.log", Contains: "ready", Timeout: time.Second}
	command := applyStandaloneCommand(service, []string{"TOKEN=super-secret"}, "hash-1")
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("existing-1", service.Name, "hash-1", "running")}, nil).Once()
	runtime.On("CopyFromContainer", mock.Anything, "existing-1", "/logs/server.log").Return(io.NopCloser(strings.NewReader("ready")), nil).Once()

	result, err := NewLocalRuntimeStandaloneServiceManager(runtime).ApplyStandaloneService(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	assert.Nil(t, result.Error)
}

func TestLocalRuntimeStandaloneServiceManagerApplyDoesNotLeakSecretInFailure(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	command := applyStandaloneCommand(service, []string{"TOKEN=super-secret"}, "hash-1")
	runtime.On("ListContainers", mock.Anything, true).Return(nil, errors.New("docker rejected TOKEN=super-secret")).Once()

	result, err := NewLocalRuntimeStandaloneServiceManager(runtime).ApplyStandaloneService(context.Background(), command)

	require.NoError(t, err)
	require.NotNil(t, result.Error)
	assert.Equal(t, "runtime_command_failed", result.Error.Code)
	assert.NotContains(t, result.Error.Message, "super-secret")
	assert.NotContains(t, result.Error.Message, "TOKEN")
}

func TestLocalRuntimeStandaloneServiceManagerRemoveUsesCommandCleanupForNonRemovedReasons(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		runtime := outmocks.NewMockContainerRuntime(t)
		container := managedContainer("removed-1", "game", "old-hash", "running")
		container.Labels[domain.LabelServiceCleanupPreserveVolumes] = "true"
		container.Labels[domain.LabelServiceCleanupRemoveContainer] = "false"
		container.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-game-data"
		container.VolumeMounts = []domain.ContainerVolumeMount{{Name: "gordon-service-game-data", Type: "volume"}, {Name: "user-volume", Type: "volume"}}
		runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
		runtime.On("StopContainer", mock.Anything, "removed-1").Return(nil).Once()
		runtime.On("RemoveContainer", mock.Anything, "removed-1", true).Return(nil).Once()
		runtime.On("RemoveVolume", mock.Anything, "gordon-service-game-data", true).Return(nil).Once()

		result, err := NewLocalRuntimeStandaloneServiceManager(runtime).RemoveStandaloneService(context.Background(), removeStandaloneCommandWithCleanup("game", "disabled", domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}))

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	})
	for _, reason := range []string{"", "malformed"} {
		t.Run("conservative_"+reason, func(t *testing.T) {
			runtime := outmocks.NewMockContainerRuntime(t)
			container := managedContainer("removed-1", "game", "old-hash", "running")
			container.Labels[domain.LabelServiceCleanupPreserveVolumes] = "false"
			container.Labels[domain.LabelServiceCleanupRemoveContainer] = "true"
			container.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-game-data"
			container.VolumeMounts = []domain.ContainerVolumeMount{{Name: "gordon-service-game-data", Type: "volume"}}
			runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
			runtime.On("StopContainer", mock.Anything, "removed-1").Return(nil).Once()

			result, err := NewLocalRuntimeStandaloneServiceManager(runtime).RemoveStandaloneService(context.Background(), removeStandaloneCommandWithCleanup("game", reason, domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: false}))

			require.NoError(t, err)
			assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
		})
	}
}

func TestLocalRuntimeStandaloneServiceManagerRemoveUsesPerContainerCleanupLabelsForRemovedDuplicates(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	removeAndDeleteVolume := managedContainer("removed-1", "game", "old-hash", "running")
	removeAndDeleteVolume.Labels[domain.LabelServiceCleanupPreserveVolumes] = "false"
	removeAndDeleteVolume.Labels[domain.LabelServiceCleanupRemoveContainer] = "true"
	removeAndDeleteVolume.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-game-first"
	removeAndDeleteVolume.VolumeMounts = []domain.ContainerVolumeMount{
		{Name: "gordon-service-game-first", Type: "volume"},
		{Name: "unmanaged-volume", Type: "volume"},
	}
	preserveAndKeepContainer := managedContainer("removed-2", "game", "old-hash", "running")
	preserveAndKeepContainer.Labels[domain.LabelServiceCleanupPreserveVolumes] = "true"
	preserveAndKeepContainer.Labels[domain.LabelServiceCleanupRemoveContainer] = "false"
	preserveAndKeepContainer.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-game-second"
	preserveAndKeepContainer.VolumeMounts = []domain.ContainerVolumeMount{
		{Name: "gordon-service-game-second", Type: "volume"},
		{Name: "unmanaged-volume", Type: "volume"},
	}
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{removeAndDeleteVolume, preserveAndKeepContainer}, nil).Once()
	runtime.On("StopContainer", mock.Anything, "removed-1").Return(nil).Once()
	runtime.On("RemoveContainer", mock.Anything, "removed-1", true).Return(nil).Once()
	runtime.On("RemoveVolume", mock.Anything, "gordon-service-game-first", true).Return(nil).Once()
	runtime.On("StopContainer", mock.Anything, "removed-2").Return(nil).Once()

	result, err := NewLocalRuntimeStandaloneServiceManager(runtime).RemoveStandaloneService(context.Background(), removeStandaloneCommandWithCleanup("game", "removed", domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}))

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
}

func TestLocalRuntimeStandaloneServiceManagerListStateIsSanitizedAndDeterministic(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{
		managedContainer("id-2", "beta", "hash-2", "running"),
		managedContainer("id-2", "alpha", "hash-2", "exited"),
		managedContainer("id-1", "alpha", "hash-1", "running"),
		{ID: "unmanaged", Labels: map[string]string{domain.LabelService: "false"}},
	}, nil).Once()

	states, err := NewLocalRuntimeStandaloneServiceManager(runtime).ListStandaloneServiceState(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []domain.RuntimeStandaloneServiceState{
		{Name: "alpha", ContainerID: "id-1", ContainerName: "gordon-service-alpha", Status: domain.ContainerStatusRunning, ConfigHash: "hash-1", Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true}},
		{Name: "alpha", ContainerID: "id-2", ContainerName: "gordon-service-alpha", Status: domain.ContainerStatusExited, ConfigHash: "hash-2", Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true}},
		{Name: "beta", ContainerID: "id-2", ContainerName: "gordon-service-beta", Status: domain.ContainerStatusRunning, ConfigHash: "hash-2", Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true}},
	}, states)
}

func TestLocalRuntimeStandaloneServiceManagerStateParsesCleanupLabels(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	container := managedContainer("container-1", "game", "hash-1", "running")
	container.Labels[domain.LabelServiceCleanupPreserveVolumes] = "false"
	container.Labels[domain.LabelServiceCleanupRemoveContainer] = "false"
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()

	states, err := NewLocalRuntimeStandaloneServiceManager(runtime).ListStandaloneServiceState(context.Background())

	require.NoError(t, err)
	require.Len(t, states, 1)
	assert.Equal(t, domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: false}, states[0].Cleanup)
}

func TestLocalRuntimeStandaloneServiceManagerTCPReadinessSupportsIPv6AndContext(t *testing.T) {
	service := sampleService().ForRuntimeApply()
	service.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessTCP, Timeout: time.Second}
	service.Ports = []domain.StandaloneServicePort{{Name: "admin", Container: 1, Protocol: domain.NetworkProtocolTCP, Publish: "[::]:38016"}}
	address, err := tcpReadinessAddress(service)
	require.NoError(t, err)
	assert.Equal(t, "[::1]:38016", address)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
			close(accepted)
		}
	}()
	service.Ports = []domain.StandaloneServicePort{{Name: "admin", Container: 1, Protocol: domain.NetworkProtocolTCP, Publish: listener.Addr().String()}}
	runtime := outmocks.NewMockContainerRuntime(t)
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("container-1", service.Name, "hash-1", "running")}, nil).Once()
	result, err := NewLocalRuntimeStandaloneServiceManager(runtime).ApplyStandaloneService(context.Background(), applyStandaloneCommand(service, nil, "hash-1"))
	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	<-accepted

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.Ports[0].Publish = "127.0.0.1:1"
	err = waitTCPReadiness(ctx, service)
	require.ErrorIs(t, err, context.Canceled)
}

func TestLocalRuntimeStandaloneServiceManagerLogReadinessTimeoutSanitizesReadErrors(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	service.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/server.log", Contains: "ready", Timeout: 25 * time.Millisecond}
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("container-1", service.Name, "hash-1", "running")}, nil).Once()
	runtime.On("CopyFromContainer", mock.Anything, "container-1", "/logs/server.log").Return(nil, errors.New("TOKEN=super-secret read failed"))

	result, err := NewLocalRuntimeStandaloneServiceManager(runtime).ApplyStandaloneService(context.Background(), applyStandaloneCommand(service, []string{"TOKEN=super-secret"}, "hash-1"))

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusFailed, result.Status)
	require.NotNil(t, result.Error)
	assert.Equal(t, "context_deadline_exceeded", result.Error.Code)
	assert.NotContains(t, result.Error.Message, "super-secret")
}

func TestForRuntimeApplyKeepsExistingConfigHashInvariant(t *testing.T) {
	service := sampleService()
	service.Env = []string{"PUBLIC=value", "TOKEN=super-secret"}
	before, err := serviceConfigHashWithEnv(service, []string{"PUBLIC=value", "TOKEN=super-secret"})
	require.NoError(t, err)
	after, err := serviceConfigHashWithEnv(service.ForRuntimeApply(), []string{"PUBLIC=value", "TOKEN=super-secret"})
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestLocalRuntimeStandaloneServiceManagerDoesNotCollideNamedServicesWithMatchingConfigHash(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	first := sampleService().ForRuntimeApply()
	first.Name = "alpha"
	second := first
	second.Name = "beta"
	hash, err := serviceConfigHashWithEnv(first, []string{"PUBLIC=value"})
	require.NoError(t, err)
	secondHash, err := serviceConfigHashWithEnv(second, []string{"PUBLIC=value"})
	require.NoError(t, err)
	require.Equal(t, hash, secondHash)
	var created []string
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{}, nil).Times(2)
	runtime.On("InspectImageVolumes", mock.Anything, first.Image).Return([]string{}, nil).Times(2)
	runtime.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Run(func(arguments mock.Arguments) {
		created = append(created, arguments.Get(1).(*domain.ContainerConfig).Name)
	}).Return(&domain.Container{ID: "created"}, nil).Times(2)
	runtime.On("StartContainer", mock.Anything, "created").Return(nil).Times(2)
	manager := NewLocalRuntimeStandaloneServiceManager(runtime)

	firstResult, err := manager.ApplyStandaloneService(context.Background(), applyStandaloneCommand(first, []string{"PUBLIC=value"}, hash))
	require.NoError(t, err)
	secondResult, err := manager.ApplyStandaloneService(context.Background(), applyStandaloneCommand(second, []string{"PUBLIC=value"}, hash))
	require.NoError(t, err)

	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, firstResult.Status)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, secondResult.Status)
	assert.Equal(t, []string{"gordon-service-alpha", "gordon-service-beta"}, created)
}

func TestServiceReconcileRepeatsReadinessForMatchingRunningService(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService()
	service.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/server.log", Contains: "ready", Timeout: time.Second}
	hash, err := serviceConfigHash(service)
	require.NoError(t, err)
	container := managedContainer("existing-1", service.Name, hash, "running")
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Times(4)
	runtime.On("CopyFromContainer", mock.Anything, "existing-1", "/logs/server.log").Return(func(context.Context, string, string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("ready")), nil
	}).Twice()
	control := NewService(runtime)

	require.NoError(t, control.Reconcile(context.Background(), []domain.StandaloneService{service}))
	require.NoError(t, control.Reconcile(context.Background(), []domain.StandaloneService{service}))
}

func TestLocalRuntimeStandaloneServiceManagerCoalescesOnlyConcurrentDuplicates(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	command := applyStandaloneCommand(service, []string{"PUBLIC=value"}, "hash-1")
	started := make(chan struct{})
	unblock := make(chan struct{})
	runtime.On("ListContainers", mock.Anything, true).Run(func(mock.Arguments) {
		close(started)
		<-unblock
	}).Return([]*domain.Container{}, nil).Once()
	runtime.On("InspectImageVolumes", mock.Anything, service.Image).Return([]string{}, nil).Once()
	runtime.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{ID: "created"}, nil).Once()
	runtime.On("StartContainer", mock.Anything, "created").Return(nil).Once()
	manager := NewLocalRuntimeStandaloneServiceManager(runtime)
	type outcome struct {
		result domain.RuntimeCommandResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	go func() {
		result, applyErr := manager.ApplyStandaloneService(context.Background(), command)
		outcomes <- outcome{result, applyErr}
	}()
	<-started
	go func() {
		result, applyErr := manager.ApplyStandaloneService(context.Background(), command)
		outcomes <- outcome{result, applyErr}
	}()
	close(unblock)

	first := <-outcomes
	second := <-outcomes
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, first.result.Status)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, second.result.Status)
}

func TestLocalRuntimeStandaloneServiceManagerSerializesSameServiceApplyMutations(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	first := applyStandaloneCommand(service, nil, "hash-a")
	second := applyStandaloneCommand(service, nil, "hash-b")
	second.RuntimeCommandIdentity = standaloneServiceIdentity("apply-b")
	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	secondListCalled := make(chan struct{})
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("first", service.Name, "hash-a", "exited")}, nil).Once()
	runtime.On("StartContainer", mock.Anything, "first").Run(func(mock.Arguments) {
		close(mutationStarted)
		<-releaseMutation
	}).Return(nil).Once()
	runtime.On("ListContainers", mock.Anything, true).Run(func(mock.Arguments) {
		close(secondListCalled)
	}).Return([]*domain.Container{}, nil).Once()
	runtime.On("InspectImageVolumes", mock.Anything, service.Image).Return([]string{}, nil).Once()
	runtime.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{ID: "second"}, nil).Once()
	runtime.On("StartContainer", mock.Anything, "second").Return(nil).Once()
	manager := newLocalRuntimeStandaloneServiceManager(runtime, defaultVolumePrefix)
	outcomes := make(chan error, 2)
	go func() { _, err := manager.ApplyStandaloneService(context.Background(), first); outcomes <- err }()
	<-mutationStarted
	go func() { _, err := manager.ApplyStandaloneService(context.Background(), second); outcomes <- err }()
	waitForServiceGateReferences(t, manager, service.Name, 2)
	select {
	case <-secondListCalled:
		t.Fatal("same-service apply listed containers while another mutation was active")
	default:
	}
	close(releaseMutation)
	require.NoError(t, <-outcomes)
	require.NoError(t, <-outcomes)
	<-secondListCalled
}

func TestLocalRuntimeStandaloneServiceManagerSerializesSameServiceApplyAndRemove(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	apply := applyStandaloneCommand(service, nil, "hash-a")
	remove := removeStandaloneCommandWithCleanup(service.Name, "disabled", domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: false})
	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	removeListCalled := make(chan struct{})
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("first", service.Name, "hash-a", "exited")}, nil).Once()
	runtime.On("StartContainer", mock.Anything, "first").Run(func(mock.Arguments) {
		close(mutationStarted)
		<-releaseMutation
	}).Return(nil).Once()
	runtime.On("ListContainers", mock.Anything, true).Run(func(mock.Arguments) {
		close(removeListCalled)
	}).Return([]*domain.Container{}, nil).Once()
	manager := newLocalRuntimeStandaloneServiceManager(runtime, defaultVolumePrefix)
	outcomes := make(chan error, 2)
	go func() { _, err := manager.ApplyStandaloneService(context.Background(), apply); outcomes <- err }()
	<-mutationStarted
	go func() { _, err := manager.RemoveStandaloneService(context.Background(), remove); outcomes <- err }()
	waitForServiceGateReferences(t, manager, service.Name, 2)
	select {
	case <-removeListCalled:
		t.Fatal("same-service remove listed containers while an apply mutation was active")
	default:
	}
	close(releaseMutation)
	require.NoError(t, <-outcomes)
	require.NoError(t, <-outcomes)
	<-removeListCalled
}

func TestLocalRuntimeStandaloneServiceManagerAllowsDifferentServiceMutationsConcurrently(t *testing.T) {
	manager := newLocalRuntimeStandaloneServiceManager(nil, defaultVolumePrefix)
	alphaEntered := make(chan struct{})
	betaEntered := make(chan struct{})
	release := make(chan struct{})
	outcomes := make(chan error, 2)
	go func() {
		_, err := manager.runOnce(context.Background(), standaloneServiceIdentity("alpha"), "apply_standalone_service", "alpha", func() error {
			close(alphaEntered)
			<-release
			return nil
		})
		outcomes <- err
	}()
	<-alphaEntered
	go func() {
		_, err := manager.runOnce(context.Background(), standaloneServiceIdentity("beta"), "apply_standalone_service", "beta", func() error {
			close(betaEntered)
			<-release
			return nil
		})
		outcomes <- err
	}()
	<-betaEntered
	close(release)
	require.NoError(t, <-outcomes)
	require.NoError(t, <-outcomes)
}

func TestLocalRuntimeStandaloneServiceManagerServiceGateHonorsCancellationAndCleansUp(t *testing.T) {
	manager := newLocalRuntimeStandaloneServiceManager(nil, defaultVolumePrefix)
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = manager.runOnce(context.Background(), standaloneServiceIdentity("first"), "apply_standalone_service", "game", func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan domain.RuntimeCommandResult, 1)
	operationEntered := make(chan struct{})
	go func() {
		result, _ := manager.runOnce(ctx, standaloneServiceIdentity("waiting"), "remove_standalone_service", "game", func() error {
			close(operationEntered)
			return nil
		})
		waiting <- result
	}()
	waitForServiceGateReferences(t, manager, "game", 2)
	cancel()
	result := <-waiting
	assert.Equal(t, domain.RuntimeCommandStatusFailed, result.Status)
	assert.Equal(t, "context_canceled", result.Error.Code)
	select {
	case <-operationEntered:
		t.Fatal("canceled gate waiter entered its operation")
	default:
	}
	waitForServiceGateReferences(t, manager, "game", 1)
	close(releaseFirst)
	<-firstDone
	waitForServiceGateCount(t, manager, 0)
}

func standaloneServiceIdentity(key string) domain.RuntimeCommandIdentity {
	return domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID(key), IdempotencyKey: key, Generation: 1, SourceComponentID: "control-1"}
}

func waitForServiceGateReferences(t *testing.T, manager *localRuntimeStandaloneServiceManager, service string, want int) {
	t.Helper()
	for {
		manager.mu.Lock()
		gate := manager.serviceGates[service]
		references := 0
		if gate != nil {
			references = gate.references
		}
		manager.mu.Unlock()
		if references == want {
			return
		}
		runtime.Gosched()
	}
}

func waitForServiceGateCount(t *testing.T, manager *localRuntimeStandaloneServiceManager, want int) {
	t.Helper()
	for {
		manager.mu.Lock()
		count := len(manager.serviceGates)
		manager.mu.Unlock()
		if count == want {
			return
		}
		runtime.Gosched()
	}
}

func TestLocalRuntimeStandaloneServiceManagerRetriesFailedCommands(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	service := sampleService().ForRuntimeApply()
	command := applyStandaloneCommand(service, []string{"PUBLIC=value"}, "hash-1")
	runtime.On("ListContainers", mock.Anything, true).Return(nil, errors.New("temporary runtime failure")).Once()
	runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{}, nil).Once()
	runtime.On("InspectImageVolumes", mock.Anything, service.Image).Return([]string{}, nil).Once()
	runtime.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{ID: "created"}, nil).Once()
	runtime.On("StartContainer", mock.Anything, "created").Return(nil).Once()
	manager := NewLocalRuntimeStandaloneServiceManager(runtime)

	failed, err := manager.ApplyStandaloneService(context.Background(), command)
	require.NoError(t, err)
	retried, err := manager.ApplyStandaloneService(context.Background(), command)

	require.NoError(t, err)
	assert.Equal(t, domain.RuntimeCommandStatusFailed, failed.Status)
	assert.Equal(t, domain.RuntimeCommandStatusSucceeded, retried.Status)
}

func applyStandaloneCommand(service domain.StandaloneService, env []string, hash string) domain.ApplyStandaloneServiceCommand {
	return domain.ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "command-apply", IdempotencyKey: "apply-game-hash", Generation: 1, SourceComponentID: "control-1"},
		Service:                service,
		ResolvedEnv:            env,
		ConfigHash:             hash,
	}
}

func removeStandaloneCommandWithCleanup(name, reason string, cleanup domain.StandaloneServiceCleanup) domain.RemoveStandaloneServiceCommand {
	return domain.RemoveStandaloneServiceCommand{RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "command-remove", IdempotencyKey: "remove-game", Generation: 1, SourceComponentID: "control-1"}, Name: name, Reason: reason, Cleanup: cleanup}
}
