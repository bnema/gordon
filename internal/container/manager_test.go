package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"gordon/internal/config"
	"gordon/internal/testutils"
	"gordon/internal/testutils/mocks"
	"gordon/pkg/runtime"
)

func TestManager_GetContainer(t *testing.T) {
	container := &runtime.Container{
		ID:   "test123",
		Name: "test-container",
	}

	manager := &Manager{
		containers: map[string]*runtime.Container{
			"test.example.com": container,
		},
	}

	// Test existing container
	found, exists := manager.GetContainer("test.example.com")
	assert.True(t, exists)
	assert.Equal(t, container, found)

	// Test non-existent container
	notFound, exists := manager.GetContainer("nonexistent.example.com")
	assert.False(t, exists)
	assert.Nil(t, notFound)
}

func TestManager_ListContainers(t *testing.T) {
	container1 := &runtime.Container{ID: "test1", Name: "container1"}
	container2 := &runtime.Container{ID: "test2", Name: "container2"}

	manager := &Manager{
		containers: map[string]*runtime.Container{
			"app1.example.com": container1,
			"app2.example.com": container2,
		},
	}

	containers := manager.ListContainers()

	assert.Len(t, containers, 2)
	assert.Contains(t, containers, "app1.example.com")
	assert.Contains(t, containers, "app2.example.com")
	assert.Equal(t, container1, containers["app1.example.com"])
	assert.Equal(t, container2, containers["app2.example.com"])
}

func TestManager_HealthCheck(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)

	mockRuntime := mocks.NewMockRuntime(ctrl)
	mockRuntime.EXPECT().IsContainerRunning(ctx, "container1").Return(true, nil)
	mockRuntime.EXPECT().IsContainerRunning(ctx, "container2").Return(false, nil)

	manager := &Manager{
		runtime: mockRuntime,
		containers: map[string]*runtime.Container{
			"app1.example.com": {ID: "container1"},
			"app2.example.com": {ID: "container2"},
		},
	}

	health := manager.HealthCheck(ctx)

	assert.Len(t, health, 2)
	assert.True(t, health["app1.example.com"])
	assert.False(t, health["app2.example.com"])
}

func TestManager_GetNetworkForApp(t *testing.T) {
	manager := &Manager{
		config: &config.Config{
			NetworkIsolation: config.NetworkIsolationConfig{
				Enabled:       true,
				NetworkPrefix: "gordon",
			},
			NetworkGroups: map[string][]string{
				"backend": {"api.example.com", "worker.example.com"},
			},
		},
	}

	// Test app in network group
	network := manager.GetNetworkForApp("api.example.com")
	assert.Equal(t, "gordon-backend", network)

	// Test standalone app
	network = manager.GetNetworkForApp("frontend.example.com")
	assert.Equal(t, "gordon-frontend-example-com", network)

	// Test disabled network isolation
	manager.config.NetworkIsolation.Enabled = false
	network = manager.GetNetworkForApp("test.example.com")
	assert.Equal(t, "bridge", network)
}

func TestManager_Runtime(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := mocks.NewMockRuntime(ctrl)
	manager := &Manager{
		runtime: mockRuntime,
	}

	result := manager.Runtime()
	assert.Equal(t, mockRuntime, result)
}

func TestManager_DeployContainer(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
		},
		Volumes: config.VolumeConfig{
			AutoCreate: true,
			Prefix:     "gordon",
			Preserve:   false,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled:       true,
			NetworkPrefix: "gordon",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	route := config.Route{
		Domain: "test.example.com",
		Image:  "nginx:latest",
	}

	// Set up expectations for successful deployment
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{"nginx:latest"}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "nginx:latest").Return([]int{80}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "nginx:latest").Return([]string{"PATH=/usr/local/sbin:/usr/local/bin"}, nil)
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "nginx:latest").Return([]string{"/var/log/nginx"}, nil)
	varLogNginxVolume := generateVolumeName("gordon", "test.example.com", "/var/log/nginx")
	mockRuntime.EXPECT().VolumeExists(ctx, varLogNginxVolume).Return(false, nil)
	mockRuntime.EXPECT().CreateVolume(ctx, varLogNginxVolume).Return(nil)
	mockRuntime.EXPECT().NetworkExists(ctx, "gordon-test-example-com").Return(false, nil)
	mockRuntime.EXPECT().CreateNetwork(ctx, "gordon-test-example-com", gomock.Any()).Return(nil)

	expectedContainer := &runtime.Container{
		ID:     "container123",
		Name:   "gordon-test.example.com",
		Image:  "nginx:latest",
		Status: "running",
		Ports:  []int{80},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "container123").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "container123").Return(expectedContainer, nil)

	container, err := manager.DeployContainer(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, expectedContainer, container)
	assert.Equal(t, expectedContainer, manager.containers["test.example.com"])
}
