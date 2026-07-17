package services

import (
	"context"
	"errors"
	"io"
	"net"
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

func TestLocalRuntimeStandaloneServiceManagerRemoveCleansManagedVolumesAndPreservesRequestedVolumes(t *testing.T) {
	t.Run("cleanup", func(t *testing.T) {
		runtime := outmocks.NewMockContainerRuntime(t)
		container := managedContainer("removed-1", "game", "old-hash", "running")
		container.Labels[domain.LabelServiceCleanupPreserveVolumes] = "false"
		container.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-game-data"
		container.VolumeMounts = []domain.ContainerVolumeMount{{Name: "gordon-service-game-data", Type: "volume"}, {Name: "user-volume", Type: "volume"}}
		runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
		runtime.On("StopContainer", mock.Anything, "removed-1").Return(nil).Once()
		runtime.On("RemoveContainer", mock.Anything, "removed-1", true).Return(nil).Once()
		runtime.On("RemoveVolume", mock.Anything, "gordon-service-game-data", true).Return(nil).Once()

		result, err := NewLocalRuntimeStandaloneServiceManager(runtime).RemoveStandaloneService(context.Background(), removeStandaloneCommand("game"))

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	})
	t.Run("preserve", func(t *testing.T) {
		runtime := outmocks.NewMockContainerRuntime(t)
		container := managedContainer("removed-1", "game", "old-hash", "exited")
		container.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-game-data"
		container.VolumeMounts = []domain.ContainerVolumeMount{{Name: "gordon-service-game-data", Type: "volume"}}
		runtime.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
		runtime.On("RemoveContainer", mock.Anything, "removed-1", true).Return(nil).Once()

		result, err := NewLocalRuntimeStandaloneServiceManager(runtime).RemoveStandaloneService(context.Background(), removeStandaloneCommand("game"))

		require.NoError(t, err)
		assert.Equal(t, domain.RuntimeCommandStatusSucceeded, result.Status)
	})
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
		{Name: "alpha", ContainerID: "id-1", ContainerName: "gordon-service-alpha", Status: domain.ContainerStatusRunning, ConfigHash: "hash-1"},
		{Name: "alpha", ContainerID: "id-2", ContainerName: "gordon-service-alpha", Status: domain.ContainerStatusExited, ConfigHash: "hash-2"},
		{Name: "beta", ContainerID: "id-2", ContainerName: "gordon-service-beta", Status: domain.ContainerStatusRunning, ConfigHash: "hash-2"},
	}, states)
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

func applyStandaloneCommand(service domain.StandaloneService, env []string, hash string) domain.ApplyStandaloneServiceCommand {
	return domain.ApplyStandaloneServiceCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "command-apply", IdempotencyKey: "apply-game-hash", Generation: 1, SourceComponentID: "control-1"},
		Service:                service,
		ResolvedEnv:            env,
		ConfigHash:             hash,
	}
}

func removeStandaloneCommand(name string) domain.RemoveStandaloneServiceCommand {
	return domain.RemoveStandaloneServiceCommand{RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: "command-remove", IdempotencyKey: "remove-game", Generation: 1, SourceComponentID: "control-1"}, Name: name}
}
