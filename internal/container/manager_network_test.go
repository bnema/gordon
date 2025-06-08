package container

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"gordon/internal/config"
	"gordon/internal/testutils"
	"gordon/internal/testutils/mocks"
	"gordon/pkg/runtime"
)

func TestManager_GetNetworkForApp_WithGroups(t *testing.T) {
	manager := &Manager{
		config: &config.Config{
			NetworkIsolation: config.NetworkIsolationConfig{
				Enabled:       true,
				NetworkPrefix: "gordon",
			},
			NetworkGroups: map[string][]string{
				"backend": {"api.example.com", "worker.example.com"},
				"frontend": {"web.example.com", "cdn.example.com"},
			},
		},
	}

	// Test app in backend group
	network := manager.GetNetworkForApp("api.example.com")
	assert.Equal(t, "gordon-backend", network)

	// Test app in frontend group
	network = manager.GetNetworkForApp("web.example.com")
	assert.Equal(t, "gordon-frontend", network)

	// Test standalone app
	network = manager.GetNetworkForApp("standalone.example.com")
	assert.Equal(t, "gordon-standalone-example-com", network)

	// Test disabled network isolation
	manager.config.NetworkIsolation.Enabled = false
	network = manager.GetNetworkForApp("test.example.com")
	assert.Equal(t, "bridge", network)
}

func TestManager_generateNetworkName(t *testing.T) {
	manager := &Manager{
		config: &config.Config{
			NetworkIsolation: config.NetworkIsolationConfig{
				NetworkPrefix: "gordon",
			},
		},
	}

	networkName := manager.generateNetworkName("test.example.com")
	assert.Equal(t, "gordon-test-example-com", networkName)

	networkName = manager.generateNetworkName("backend")
	assert.Equal(t, "gordon-backend", networkName)

	networkName = manager.generateNetworkName("complex-app.subdomain.example.com")
	assert.Equal(t, "gordon-complex-app-subdomain-example-com", networkName)
}

func TestManager_GetAppsForNetwork(t *testing.T) {
	manager := &Manager{
		config: &config.Config{
			NetworkGroups: map[string][]string{
				"backend": {"api.example.com", "worker.example.com"},
				"frontend": {"web.example.com"},
			},
		},
	}

	// Test network group
	apps := manager.GetAppsForNetwork("backend")
	assert.Equal(t, []string{"api.example.com", "worker.example.com"}, apps)

	// Test single app group
	apps = manager.GetAppsForNetwork("frontend")
	assert.Equal(t, []string{"web.example.com"}, apps)

	// Test single app (not in any group)
	apps = manager.GetAppsForNetwork("standalone.example.com")
	assert.Equal(t, []string{"standalone.example.com"}, apps)
}

func TestManager_CreateNetworkIfNeeded(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test bridge network (should not try to create)
	err = manager.CreateNetworkIfNeeded(ctx, "bridge")
	assert.NoError(t, err)

	// Test default network (should not try to create)
	err = manager.CreateNetworkIfNeeded(ctx, "default")
	assert.NoError(t, err)

	// Test custom network that doesn't exist
	mockRuntime.EXPECT().NetworkExists(ctx, "gordon-test").Return(false, nil)
	mockRuntime.EXPECT().CreateNetwork(ctx, "gordon-test", map[string]string{"driver": "bridge"}).Return(nil)

	err = manager.CreateNetworkIfNeeded(ctx, "gordon-test")
	assert.NoError(t, err)

	// Test custom network that already exists
	mockRuntime.EXPECT().NetworkExists(ctx, "gordon-existing").Return(true, nil)

	err = manager.CreateNetworkIfNeeded(ctx, "gordon-existing")
	assert.NoError(t, err)
}

func TestManager_CreateNetworkIfNeeded_Errors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test error checking if network exists
	mockRuntime.EXPECT().NetworkExists(ctx, "gordon-test").Return(false, errors.New("check failed"))

	err = manager.CreateNetworkIfNeeded(ctx, "gordon-test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to check if network exists")

	// Test error creating network
	mockRuntime.EXPECT().NetworkExists(ctx, "gordon-test").Return(false, nil)
	mockRuntime.EXPECT().CreateNetwork(ctx, "gordon-test", map[string]string{"driver": "bridge"}).Return(errors.New("create failed"))

	err = manager.CreateNetworkIfNeeded(ctx, "gordon-test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create network")
}

func TestManager_cleanupNetworkIfEmpty(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test cleanup of empty network
	networks := []*runtime.NetworkInfo{
		{
			Name:       "gordon-test",
			Containers: []string{}, // Empty network
		},
		{
			Name:       "gordon-used",
			Containers: []string{"container1"}, // Has containers
		},
	}

	mockRuntime.EXPECT().ListNetworks(ctx).Return(networks, nil)
	mockRuntime.EXPECT().RemoveNetwork(ctx, "gordon-test").Return(nil)

	err = manager.cleanupNetworkIfEmpty(ctx, "gordon-test")
	assert.NoError(t, err)

	// Test network with containers (should not be removed)
	mockRuntime.EXPECT().ListNetworks(ctx).Return(networks, nil)

	err = manager.cleanupNetworkIfEmpty(ctx, "gordon-used")
	assert.NoError(t, err)

	// Test bridge network (should not try to remove)
	err = manager.cleanupNetworkIfEmpty(ctx, "bridge")
	assert.NoError(t, err)

	// Test default network (should not try to remove)
	err = manager.cleanupNetworkIfEmpty(ctx, "default")
	assert.NoError(t, err)
}

func TestManager_cleanupNetworkIfEmpty_Errors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test error listing networks
	mockRuntime.EXPECT().ListNetworks(ctx).Return(nil, errors.New("list failed"))

	err = manager.cleanupNetworkIfEmpty(ctx, "gordon-test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list networks")

	// Test error removing network
	networks := []*runtime.NetworkInfo{
		{
			Name:       "gordon-test",
			Containers: []string{},
		},
	}

	mockRuntime.EXPECT().ListNetworks(ctx).Return(networks, nil)
	mockRuntime.EXPECT().RemoveNetwork(ctx, "gordon-test").Return(errors.New("remove failed"))

	err = manager.cleanupNetworkIfEmpty(ctx, "gordon-test")
	assert.Error(t, err)
}

func TestManager_DeployAttachedService_SharedService(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			AutoCreate: false,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			NetworkPrefix: "gordon",
		},
		NetworkGroups: map[string][]string{
			"backend": {"api.example.com", "worker.example.com"},
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test deploying shared service for network group
	mockRuntime.EXPECT().ListContainers(ctx, false).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "postgres:latest").Return([]int{5432}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "postgres:latest").Return([]string{"POSTGRES_VERSION=15"}, nil)

	expectedContainer := &runtime.Container{
		ID:   "postgres-service",
		Name: "gordon-shared-postgres",
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "postgres-service").Return(nil)

	err = manager.DeployAttachedService(ctx, "backend", "postgres:latest")

	assert.NoError(t, err)
}

func TestManager_DeployAttachedService_AppSpecific(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			AutoCreate: false,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			NetworkPrefix: "gordon",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test deploying app-specific service
	mockRuntime.EXPECT().ListContainers(ctx, false).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "redis:latest").Return([]int{6379}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "redis:latest").Return([]string{}, nil)

	expectedContainer := &runtime.Container{
		ID:   "redis-service",
		Name: "api-example-com-redis",
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "redis-service").Return(nil)

	err = manager.DeployAttachedService(ctx, "api.example.com", "redis:latest")

	assert.NoError(t, err)
}

func TestManager_DeployAttachedService_AlreadyRunning(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		NetworkIsolation: config.NetworkIsolationConfig{
			NetworkPrefix: "gordon",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Mock existing container with same name
	existingContainers := []*runtime.Container{
		{
			ID:   "existing-redis",
			Name: "api-example-com-redis",
		},
	}

	mockRuntime.EXPECT().ListContainers(ctx, false).Return(existingContainers, nil)

	err = manager.DeployAttachedService(ctx, "api.example.com", "redis:latest")

	assert.NoError(t, err)
}

func TestManager_DeployAttachedService_WithVolumes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			AutoCreate: true,
			Prefix:     "gordon",
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			NetworkPrefix: "gordon",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test service with volumes
	mockRuntime.EXPECT().ListContainers(ctx, false).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "postgres:latest").Return([]int{5432}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "postgres:latest").Return([]string{}, nil)
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "postgres:latest").Return([]string{"/var/lib/postgresql/data"}, nil)

	// Volume creation expectations
	mockRuntime.EXPECT().VolumeExists(ctx, "gordon-api-example-com-var-lib-postgresql-data").Return(false, nil)
	mockRuntime.EXPECT().CreateVolume(ctx, "gordon-api-example-com-var-lib-postgresql-data").Return(nil)

	expectedContainer := &runtime.Container{
		ID:   "postgres-service",
		Name: "api-example-com-postgres",
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "postgres-service").Return(nil)

	err = manager.DeployAttachedService(ctx, "api.example.com", "postgres:latest")

	assert.NoError(t, err)
}

func TestManager_DeployAttachedService_Errors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			AutoCreate: false,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			NetworkPrefix: "gordon",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test create container error
	mockRuntime.EXPECT().ListContainers(ctx, false).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "redis:latest").Return([]int{6379}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "redis:latest").Return([]string{}, nil)
	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(nil, errors.New("create failed"))

	err = manager.DeployAttachedService(ctx, "api.example.com", "redis:latest")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create service container")

	// Test start container error
	mockRuntime.EXPECT().ListContainers(ctx, false).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "redis:latest").Return([]int{6379}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "redis:latest").Return([]string{}, nil)

	container := &runtime.Container{ID: "service123", Name: "api-example-com-redis"}
	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(container, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "service123").Return(errors.New("start failed"))
	mockRuntime.EXPECT().RemoveContainer(ctx, "service123", true).Return(nil)

	err = manager.DeployAttachedService(ctx, "api.example.com", "redis:latest")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to start service container")
}