package services

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/secrets"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestService_ReconcileCreatesMissingEnabledService(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	var created *domain.ContainerConfig
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{}, nil).Once()
	rt.On("InspectImageVolumes", mock.Anything, svc.Image).Return([]string{"/data"}, nil).Once()
	rt.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Run(func(args mock.Arguments) {
		created = args.Get(1).(*domain.ContainerConfig)
	}).Return(&domain.Container{ID: "created-1"}, nil).Once()
	rt.On("StartContainer", mock.Anything, "created-1").Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, "game:latest", created.Image)
	assert.Equal(t, "gordon-service-game", created.Name)
	assert.Equal(t, []string{"PUBLIC=value"}, created.Env)
	assert.Equal(t, domain.RestartPolicyAlways, created.RestartPolicy)
	assert.Equal(t, "true", created.Labels[domain.LabelManaged])
	assert.Equal(t, "true", created.Labels[domain.LabelService])
	assert.Equal(t, "game", created.Labels[domain.LabelServiceName])
	assert.Equal(t, "true", created.Labels[domain.LabelServiceCleanupPreserveVolumes])
	assert.Equal(t, "true", created.Labels[domain.LabelServiceCleanupRemoveContainer])
	assert.NotEmpty(t, created.Labels[domain.LabelServiceConfigHash])
	assert.Equal(t, "gordon-service-game-data", created.Labels[domain.LabelServiceManagedVolumes])
	assert.Equal(t, []domain.ContainerPortPublish{{HostIP: "127.0.0.1", HostPort: 38015, ContainerPort: 28015, Protocol: domain.NetworkProtocolUDP}}, created.PortPublishes)
	assert.Equal(t, map[string]string{"/data": "gordon-service-game-data"}, created.Volumes)
}

func TestService_ReconcileStartsStoppedExistingService(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	hash, err := serviceConfigHash(svc)
	require.NoError(t, err)
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("existing-1", svc.Name, hash, "exited")}, nil).Once()
	rt.On("StartContainer", mock.Anything, "existing-1").Return(nil).Once()

	err = NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
}

func TestService_ReconcileChecksReadinessForExistingMatchingService(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	svc.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/server.log", Contains: "ready", Timeout: time.Second}
	hash, err := serviceConfigHash(svc)
	require.NoError(t, err)
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("existing-1", svc.Name, hash, "running")}, nil).Once()
	rt.On("CopyFromContainer", mock.Anything, "existing-1", "/logs/server.log").Return(io.NopCloser(strings.NewReader("ready")), nil).Once()

	err = NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
}

func TestService_ReconcileRecreatesStaleService(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("old-1", svc.Name, "old-hash", "running")}, nil).Once()
	rt.On("StopContainer", mock.Anything, "old-1").Return(nil).Once()
	rt.On("RemoveContainer", mock.Anything, "old-1", true).Return(nil).Once()
	rt.On("InspectImageVolumes", mock.Anything, svc.Image).Return([]string{}, nil).Once()
	rt.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{ID: "new-1"}, nil).Once()
	rt.On("StartContainer", mock.Anything, "new-1").Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
}

func TestService_ReconcileRecreateDoesNotRemoveManagedVolumes(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	svc.Cleanup = domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}
	old := managedContainer("old-1", svc.Name, "old-hash", "running")
	old.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-game-data"
	old.VolumeMounts = []domain.ContainerVolumeMount{{Name: "gordon-service-game-data", Type: "volume", Destination: "/data"}}
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{old}, nil).Once()
	rt.On("StopContainer", mock.Anything, "old-1").Return(nil).Once()
	rt.On("RemoveContainer", mock.Anything, "old-1", true).Return(nil).Once()
	rt.On("InspectImageVolumes", mock.Anything, svc.Image).Return([]string{}, nil).Once()
	rt.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{ID: "new-1"}, nil).Once()
	rt.On("StartContainer", mock.Anything, "new-1").Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
}

func TestService_ReconcileRemovesDuplicateMatchingServiceContainers(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	hash, err := serviceConfigHash(svc)
	require.NoError(t, err)
	older := managedContainer("existing-1", svc.Name, hash, "running")
	duplicate := managedContainer("existing-2", svc.Name, hash, "running")
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{duplicate, older}, nil).Once()
	rt.On("StopContainer", mock.Anything, "existing-2").Return(nil).Once()
	rt.On("RemoveContainer", mock.Anything, "existing-2", true).Return(nil).Once()

	err = NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
}

func TestService_ReconcileKeepsRunningDuplicateBeforeLowerStaleID(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	hash, err := serviceConfigHash(svc)
	require.NoError(t, err)
	staleLowerID := managedContainer("existing-1", svc.Name, hash, "exited")
	runningPrimary := managedContainer("existing-2", svc.Name, hash, "running")
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{staleLowerID, runningPrimary}, nil).Once()
	rt.On("RemoveContainer", mock.Anything, "existing-1", true).Return(nil).Once()

	err = NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
}

func TestService_ReconcileStopsAndRemovesDisabledService(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	svc.Enabled = false
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{managedContainer("disabled-1", svc.Name, "old-hash", "running")}, nil).Once()
	rt.On("StopContainer", mock.Anything, "disabled-1").Return(nil).Once()
	rt.On("RemoveContainer", mock.Anything, "disabled-1", true).Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
}

func TestService_ReconcileStopsAndRemovesOmittedManagedService(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	container := managedContainer("removed-1", "removed", "old-hash", "running")
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
	rt.On("StopContainer", mock.Anything, "removed-1").Return(nil).Once()
	rt.On("RemoveContainer", mock.Anything, "removed-1", true).Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), nil)

	require.NoError(t, err)
}

func TestService_ReconcileRemovesNamedVolumeMountsWhenCleanupDoesNotPreserveVolumes(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	svc.Enabled = false
	svc.Cleanup = domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}
	container := managedContainer("disabled-1", svc.Name, "old-hash", "running")
	container.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-game-data"
	container.VolumeMounts = []domain.ContainerVolumeMount{
		{Name: "gordon-service-game-data", Type: "volume", Destination: "/data"},
		{Name: "user-shared-data", Type: "volume", Destination: "/shared"},
		{Name: "", Type: "volume", Destination: "/anonymous"},
		{Name: "host-data", Type: "bind", Destination: "/host"},
	}
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
	rt.On("StopContainer", mock.Anything, "disabled-1").Return(nil).Once()
	rt.On("RemoveContainer", mock.Anything, "disabled-1", true).Return(nil).Once()
	rt.On("RemoveVolume", mock.Anything, "gordon-service-game-data", true).Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
}

func TestService_ReconcileUsesCleanupLabelsForOmittedService(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	container := managedContainer("removed-1", "removed", "old-hash", "exited")
	container.Labels[domain.LabelServiceCleanupPreserveVolumes] = "false"
	container.Labels[domain.LabelServiceCleanupRemoveContainer] = "true"
	container.Labels[domain.LabelServiceManagedVolumes] = "gordon-service-removed-data"
	container.VolumeMounts = []domain.ContainerVolumeMount{{Name: "gordon-service-removed-data", Type: "volume", Destination: "/data"}}
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
	rt.On("RemoveContainer", mock.Anything, "removed-1", true).Return(nil).Once()
	rt.On("RemoveVolume", mock.Anything, "gordon-service-removed-data", true).Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), nil)

	require.NoError(t, err)
}

func TestService_ReconcileDefaultsOmittedServiceCleanupToPreserveVolumes(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	container := managedContainer("removed-1", "removed", "old-hash", "exited")
	container.VolumeMounts = []domain.ContainerVolumeMount{{Name: "gordon-service-removed-data", Type: "volume", Destination: "/data"}}
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{container}, nil).Once()
	rt.On("RemoveContainer", mock.Anything, "removed-1", true).Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), nil)

	require.NoError(t, err)
}

func TestService_ReconcileUsesExplicitReadOnlyVolumes(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	svc.Volumes = []domain.StandaloneServiceVolume{{Source: "cfg", Target: "/cfg", ReadOnly: true}}
	var created *domain.ContainerConfig
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{}, nil).Once()
	rt.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Run(func(args mock.Arguments) {
		created = args.Get(1).(*domain.ContainerConfig)
	}).Return(&domain.Container{ID: "created-1"}, nil).Once()
	rt.On("StartContainer", mock.Anything, "created-1").Return(nil).Once()

	err := NewService(rt).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
	assert.Nil(t, created.Volumes)
	assert.Equal(t, map[string]string{"/cfg": "cfg"}, created.ReadOnlyVolumes)
}

func TestService_StatusReturnsManagedServices(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{
		managedContainer("id-1", "alpha", "hash-1", "running"),
		{ID: "other", Labels: map[string]string{}},
	}, nil).Once()

	statuses, err := NewService(rt).Status(context.Background())

	require.NoError(t, err)
	require.Len(t, statuses, 1)
	assert.Equal(t, "alpha", statuses[0].Name)
	assert.Equal(t, "id-1", statuses[0].ContainerID)
	assert.Equal(t, domain.ContainerStatusRunning, statuses[0].Status)
	assert.Equal(t, "hash-1", statuses[0].ConfigHash)
}

func TestServiceEnvLoadsEnvFileAndMergesInlineEnv(t *testing.T) {
	envFile := writeTempEnvFile(t, "FROM_FILE=file\nOVERRIDE=file\n# ignored\n")
	svc := sampleService()
	svc.EnvFile = envFile
	svc.Env = []string{"OVERRIDE=inline", "INLINE=value"}

	env, err := NewService(nil).serviceEnv(context.Background(), svc)

	require.NoError(t, err)
	assert.Equal(t, []string{"FROM_FILE=file", "INLINE=value", "OVERRIDE=inline"}, env)
}

func TestServiceConfigHashIncludesResolvedEnvFileAndSecretValues(t *testing.T) {
	envFile := writeTempEnvFile(t, "FROM_FILE=one\n")
	provider := outmocks.NewMockSecretProvider(t)
	svc := sampleService()
	svc.EnvFile = envFile
	svc.Secrets = []domain.StandaloneServiceSecretRef{{Name: "rcon", Key: "RCON_PASSWORD"}}
	provider.On("GetSecret", mock.Anything, "service/game/rcon").Return("secret-one", nil).Once()
	first, err := NewServiceWithSecretProvider(nil, provider).serviceConfigHash(context.Background(), svc)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(envFile, []byte("FROM_FILE=two\n"), 0o600))
	provider.On("GetSecret", mock.Anything, "service/game/rcon").Return("secret-two", nil).Once()
	second, err := NewServiceWithSecretProvider(nil, provider).serviceConfigHash(context.Background(), svc)
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestService_ReconcileResolvesSecretEnvOnceForHashAndContainer(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	provider := outmocks.NewMockSecretProvider(t)
	svc := sampleService()
	svc.Secrets = []domain.StandaloneServiceSecretRef{{Name: "rcon", Key: "RCON_PASSWORD"}}
	var created *domain.ContainerConfig
	provider.On("GetSecret", mock.Anything, "service/game/rcon").Return("secret-value", nil).Once()
	rt.On("ListContainers", mock.Anything, true).Return([]*domain.Container{}, nil).Once()
	rt.On("InspectImageVolumes", mock.Anything, svc.Image).Return([]string{}, nil).Once()
	rt.On("CreateContainer", mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Run(func(args mock.Arguments) {
		created = args.Get(1).(*domain.ContainerConfig)
	}).Return(&domain.Container{ID: "created-1"}, nil).Once()
	rt.On("StartContainer", mock.Anything, "created-1").Return(nil).Once()

	err := NewServiceWithSecretProvider(rt, provider).Reconcile(context.Background(), []domain.StandaloneService{svc})

	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Contains(t, created.Env, "RCON_PASSWORD=secret-value")
	assert.NotEmpty(t, created.Labels[domain.LabelServiceConfigHash])
}

func TestServiceEnvResolvesServiceScopedSecretsWithoutLeakingValues(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	provider := outmocks.NewMockSecretProvider(t)
	svc := sampleService()
	svc.Secrets = []domain.StandaloneServiceSecretRef{{Name: "rcon", Key: "RCON_PASSWORD"}}
	provider.On("GetSecret", mock.Anything, "service/game/rcon").Return("super-secret-value", errors.New("provider failed")).Once()

	env, err := NewServiceWithSecretProvider(rt, provider).serviceEnv(context.Background(), svc)

	require.Error(t, err)
	assert.Nil(t, env)
	assert.NotContains(t, err.Error(), "super-secret-value")
}

func TestServiceSecretPathIsPassProviderSafe(t *testing.T) {
	path := serviceSecretPath("game", "rcon")

	require.NoError(t, secrets.ValidatePath(path))
	assert.Equal(t, "service/game/rcon", path)
}

func TestServiceSecretPathPreservesExplicitProviderPath(t *testing.T) {
	path := serviceSecretPath("game", "secrets/service.yaml:game.rcon")

	assert.Equal(t, "secrets/service.yaml:game.rcon", path)
}

func TestServiceEnvUsesExplicitProviderSecretPaths(t *testing.T) {
	provider := outmocks.NewMockSecretProvider(t)
	svc := sampleService()
	svc.Secrets = []domain.StandaloneServiceSecretRef{{Name: "secrets/service.yaml:game.rcon", Key: "RCON_PASSWORD"}}
	provider.On("GetSecret", mock.Anything, "secrets/service.yaml:game.rcon").Return("secret-value", nil).Once()

	env, err := NewServiceWithSecretProvider(nil, provider).serviceEnv(context.Background(), svc)

	require.NoError(t, err)
	assert.Contains(t, env, "RCON_PASSWORD=secret-value")
}

func TestTCPReadinessAddressMapsIPv6WildcardToIPv6Loopback(t *testing.T) {
	svc := sampleService()
	svc.Ports = []domain.StandaloneServicePort{{Name: "admin", Container: 28016, Protocol: domain.NetworkProtocolTCP, Publish: "[::]:38016"}}

	address, err := tcpReadinessAddress(svc)

	require.NoError(t, err)
	assert.Equal(t, "[::1]:38016", address)
}

func TestWaitTCPReadinessDialsResolvedLoopbackPublish(t *testing.T) {
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
	svc := sampleService()
	svc.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessTCP, Timeout: time.Second}
	svc.Ports = []domain.StandaloneServicePort{{Name: "admin", Container: 28016, Protocol: domain.NetworkProtocolTCP, Publish: listener.Addr().String()}}

	err = NewService(nil).waitReadiness(context.Background(), "container-1", svc)

	require.NoError(t, err)
	<-accepted
}

func TestWaitLogReadinessReadsContainerFileUntilTextAppears(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	svc.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/server.log", Contains: "ready", Timeout: time.Second}
	rt.On("CopyFromContainer", mock.Anything, "container-1", "/logs/server.log").Return(io.NopCloser(strings.NewReader("starting")), nil).Once()
	rt.On("CopyFromContainer", mock.Anything, "container-1", "/logs/server.log").Return(io.NopCloser(strings.NewReader("server ready")), nil).Once()

	err := NewService(rt).waitReadiness(context.Background(), "container-1", svc)

	require.NoError(t, err)
}

func TestWaitReadinessHonorsContextTimeout(t *testing.T) {
	svc := sampleService()
	svc.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessTCP}
	svc.Ports = []domain.StandaloneServicePort{{Name: "admin", Container: 28016, Protocol: domain.NetworkProtocolTCP, Publish: "127.0.0.1:1"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	err := NewService(nil).waitReadiness(ctx, "container-1", svc)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWaitLogReadinessPreservesLastReadErrorOnTimeout(t *testing.T) {
	rt := outmocks.NewMockContainerRuntime(t)
	svc := sampleService()
	svc.Readiness = domain.StandaloneServiceReadiness{Type: domain.StandaloneServiceReadinessLog, Path: "/logs/server.log", Contains: "ready", Timeout: 25 * time.Millisecond}
	readErr := errors.New("copy failed")
	rt.On("CopyFromContainer", mock.Anything, "container-1", "/logs/server.log").Return(nil, readErr)

	err := NewService(rt).waitReadiness(context.Background(), "container-1", svc)

	require.Error(t, err)
	assert.ErrorIs(t, err, readErr)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestNormalizeCleanupDefaultsPreserveVolumes(t *testing.T) {
	cleanup := normalizeCleanup(domain.StandaloneServiceCleanup{})

	assert.True(t, cleanup.PreserveVolumes)
	assert.True(t, cleanup.RemoveContainer)
}

func sampleService() domain.StandaloneService {
	return domain.StandaloneService{
		Name:    "game",
		Image:   "game:latest",
		Enabled: true,
		Env:     []string{"PUBLIC=value"},
		Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: true, RemoveContainer: true},
		Ports:   []domain.StandaloneServicePort{{Name: "game", Container: 28015, Protocol: domain.NetworkProtocolUDP, Publish: "127.0.0.1:38015"}},
	}
}

func managedContainer(id, name, hash, status string) *domain.Container {
	return &domain.Container{
		ID:     id,
		Name:   "gordon-service-" + name,
		Status: status,
		Labels: map[string]string{
			domain.LabelService:           "true",
			domain.LabelServiceName:       name,
			domain.LabelServiceConfigHash: hash,
		},
	}
}

func writeTempEnvFile(t *testing.T, content string) string {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "service-*.env")
	require.NoError(t, err)
	_, err = file.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	return file.Name()
}
