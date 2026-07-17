package services

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/secrets"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestServiceReconcileRoutesEnabledApplyThroughStandaloneManager(t *testing.T) {
	manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
	configured := sampleService()
	expectedHash, err := serviceConfigHash(configured)
	require.NoError(t, err)
	manager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{}, nil).Once()
	manager.On("ApplyStandaloneService", mock.Anything, mock.MatchedBy(func(command domain.ApplyStandaloneServiceCommand) bool {
		return assert.NoError(t, command.Validate()) &&
			assert.Equal(t, configured.ForRuntimeApply(), command.Service) &&
			assert.Equal(t, []string{"PUBLIC=value"}, command.ResolvedEnv) &&
			assert.Equal(t, expectedHash, command.ConfigHash) &&
			assert.NotEmpty(t, command.ID) &&
			assert.NotEmpty(t, command.IdempotencyKey) &&
			assert.NotContains(t, command.IdempotencyKey, "PUBLIC=value")
	})).Return(domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil).Once()

	err = NewServiceWithRuntimeStandaloneServiceManager(manager).Reconcile(context.Background(), []domain.StandaloneService{configured})

	require.NoError(t, err)
}

func TestServiceReconcileKeepsMatchingRunningServiceOutsideRuntimeBoundary(t *testing.T) {
	manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
	configured := sampleService()
	hash, err := serviceConfigHash(configured)
	require.NoError(t, err)
	manager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{{
		Name: configured.Name, Status: domain.ContainerStatusRunning, ConfigHash: hash,
	}}, nil).Once()

	err = NewServiceWithRuntimeStandaloneServiceManager(manager).Reconcile(context.Background(), []domain.StandaloneService{configured})

	require.NoError(t, err)
}

func TestLegacyServiceConstructorsUseLocalStandaloneManager(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	provider := outmocks.NewMockSecretProvider(t)

	assert.IsType(t, &localRuntimeStandaloneServiceManager{}, NewService(runtime).runtime)
	withSecret := NewServiceWithSecretProvider(runtime, provider)
	assert.IsType(t, &localRuntimeStandaloneServiceManager{}, withSecret.runtime)
	assert.Same(t, provider, withSecret.secretProvider)
	withPrefix := NewServiceWithVolumePrefix(runtime, "custom")
	assert.Equal(t, "custom", withPrefix.runtime.(*localRuntimeStandaloneServiceManager).volumePrefix)
}

func TestServiceReconcileRoutesDisabledAndOmittedServicesThroughStandaloneManager(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
		configured := sampleService()
		configured.Enabled = false
		configured.Cleanup = domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}
		manager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{{Name: configured.Name}}, nil).Once()
		manager.On("RemoveStandaloneService", mock.Anything, mock.MatchedBy(func(command domain.RemoveStandaloneServiceCommand) bool {
			return assert.NoError(t, command.Validate()) &&
				assert.Equal(t, configured.Name, command.Name) &&
				assert.Equal(t, "disabled", command.Reason) &&
				assert.Equal(t, domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}, command.Cleanup)
		})).Return(domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil).Once()

		err := NewServiceWithRuntimeStandaloneServiceManager(manager).Reconcile(context.Background(), []domain.StandaloneService{configured})

		require.NoError(t, err)
	})

	t.Run("omitted", func(t *testing.T) {
		manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
		manager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{{Name: "removed", Cleanup: domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}}}, nil).Once()
		manager.On("RemoveStandaloneService", mock.Anything, mock.MatchedBy(func(command domain.RemoveStandaloneServiceCommand) bool {
			return assert.NoError(t, command.Validate()) &&
				assert.Equal(t, "removed", command.Name) &&
				assert.Equal(t, "removed", command.Reason) &&
				assert.Equal(t, domain.StandaloneServiceCleanup{PreserveVolumes: false, RemoveContainer: true}, command.Cleanup)
		})).Return(domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil).Once()

		err := NewServiceWithRuntimeStandaloneServiceManager(manager).Reconcile(context.Background(), nil)

		require.NoError(t, err)
	})
}

func TestServiceReconcileResolvesSecretsOnlyInApplyCommand(t *testing.T) {
	manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
	provider := outmocks.NewMockSecretProvider(t)
	configured := sampleService()
	configured.Secrets = []domain.StandaloneServiceSecretRef{{Name: "rcon", Key: "RCON_PASSWORD"}}
	provider.On("GetSecret", mock.Anything, "service/game/rcon").Return("super-secret-value", nil).Once()
	manager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{}, nil).Once()
	manager.On("ApplyStandaloneService", mock.Anything, mock.MatchedBy(func(command domain.ApplyStandaloneServiceCommand) bool {
		return assert.Equal(t, []string{"PUBLIC=value", "RCON_PASSWORD=super-secret-value"}, command.ResolvedEnv) &&
			assert.Empty(t, command.Service.Secrets) &&
			assert.NotContains(t, command.ID, "super-secret-value") &&
			assert.NotContains(t, command.IdempotencyKey, "super-secret-value")
	})).Return(domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusSucceeded}, nil).Once()

	err := NewServiceWithRuntimeStandaloneServiceManagerAndSecretProvider(manager, provider).Reconcile(context.Background(), []domain.StandaloneService{configured})

	require.NoError(t, err)
}

func TestServiceStatusMapsAndSortsStandaloneManagerState(t *testing.T) {
	manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
	manager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{
		{Name: "zulu", ContainerID: "z", ContainerName: "gordon-service-zulu", Status: domain.ContainerStatusExited, ConfigHash: "z-hash"},
		{Name: "alpha", ContainerID: "a", ContainerName: "gordon-service-alpha", Status: domain.ContainerStatusRunning, ConfigHash: "a-hash"},
	}, nil).Once()

	statuses, err := NewServiceWithRuntimeStandaloneServiceManager(manager).Status(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []domain.StandaloneServiceStatus{
		{Name: "alpha", ContainerID: "a", ContainerName: "gordon-service-alpha", Status: domain.ContainerStatusRunning, ConfigHash: "a-hash"},
		{Name: "zulu", ContainerID: "z", ContainerName: "gordon-service-zulu", Status: domain.ContainerStatusExited, ConfigHash: "z-hash"},
	}, statuses)
}

func TestServicePropagatesStandaloneManagerErrors(t *testing.T) {
	t.Run("state", func(t *testing.T) {
		manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
		errState := errors.New("state unavailable")
		manager.On("ListStandaloneServiceState", mock.Anything).Return(nil, errState).Once()

		err := NewServiceWithRuntimeStandaloneServiceManager(manager).Reconcile(context.Background(), nil)

		require.ErrorIs(t, err, errState)
	})

	t.Run("apply", func(t *testing.T) {
		manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
		errApply := errors.New("apply unavailable")
		manager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{}, nil).Once()
		manager.On("ApplyStandaloneService", mock.Anything, mock.Anything).Return(domain.RuntimeCommandResult{}, errApply).Once()

		err := NewServiceWithRuntimeStandaloneServiceManager(manager).Reconcile(context.Background(), []domain.StandaloneService{sampleService()})

		require.ErrorIs(t, err, errApply)
	})

	t.Run("remove", func(t *testing.T) {
		manager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
		errRemove := errors.New("remove unavailable")
		manager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{{Name: "removed"}}, nil).Once()
		manager.On("RemoveStandaloneService", mock.Anything, mock.Anything).Return(domain.RuntimeCommandResult{}, errRemove).Once()

		err := NewServiceWithRuntimeStandaloneServiceManager(manager).Reconcile(context.Background(), nil)

		require.ErrorIs(t, err, errRemove)
	})
}

func TestServiceStandaloneCommandIdentitiesAreMonotonicAndInstanceNamespaced(t *testing.T) {
	firstManager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
	secondManager := outmocks.NewMockRuntimeStandaloneServiceManager(t)
	var first, second, next domain.RemoveStandaloneServiceCommand
	firstManager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{{Name: "game"}}, nil).Twice()
	firstManager.On("RemoveStandaloneService", mock.Anything, mock.Anything).Run(func(arguments mock.Arguments) {
		if first.ID == "" {
			first = arguments.Get(1).(domain.RemoveStandaloneServiceCommand)
			return
		}
		next = arguments.Get(1).(domain.RemoveStandaloneServiceCommand)
	}).Return(domain.RuntimeCommandResult{}, nil).Twice()
	secondManager.On("ListStandaloneServiceState", mock.Anything).Return([]domain.RuntimeStandaloneServiceState{{Name: "game"}}, nil).Once()
	secondManager.On("RemoveStandaloneService", mock.Anything, mock.Anything).Run(func(arguments mock.Arguments) {
		second = arguments.Get(1).(domain.RemoveStandaloneServiceCommand)
	}).Return(domain.RuntimeCommandResult{}, nil).Once()

	firstService := NewServiceWithRuntimeStandaloneServiceManager(firstManager)
	require.NoError(t, firstService.Reconcile(context.Background(), nil))
	require.NoError(t, firstService.Reconcile(context.Background(), nil))
	require.NoError(t, NewServiceWithRuntimeStandaloneServiceManager(secondManager).Reconcile(context.Background(), nil))

	assert.Equal(t, uint64(1), first.Generation)
	assert.Equal(t, uint64(2), next.Generation)
	assert.NotEqual(t, first.IdempotencyKey, next.IdempotencyKey)
	assert.Equal(t, uint64(1), second.Generation)
	assert.NotEqual(t, first.IdempotencyKey, second.IdempotencyKey)
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
