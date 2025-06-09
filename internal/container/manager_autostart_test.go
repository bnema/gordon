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

func TestManager_AutoStartContainers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
		},
		Routes: map[string]string{
			"app1.example.com": "nginx:latest",
			"app2.example.com": "apache:latest",
		},
		Volumes: config.VolumeConfig{
			AutoCreate: false,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled: false,
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Mock existing containers - one Gordon-managed running, one stopped
	existingContainers := []*runtime.Container{
		{
			ID:    "running-container",
			Name:  "gordon-app1.example.com",
			Image: "nginx:latest",
			Labels: map[string]string{
				"gordon.managed": "true",
				"gordon.domain":  "app1.example.com",
			},
		},
		{
			ID:    "stopped-container",
			Name:  "gordon-app2.example.com",
			Image: "apache:latest",
			Labels: map[string]string{
				"gordon.managed": "true",
				"gordon.domain":  "app2.example.com",
			},
		},
	}

	mockRuntime.EXPECT().ListContainers(ctx, true).Return(existingContainers, nil)

	// First container is already running
	mockRuntime.EXPECT().IsContainerRunning(ctx, "running-container").Return(true, nil)

	// Second container is stopped, should be started
	mockRuntime.EXPECT().IsContainerRunning(ctx, "stopped-container").Return(false, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "stopped-container").Return(nil)

	// Mock inspection of restarted container
	startedContainer := &runtime.Container{
		ID:    "stopped-container",
		Name:  "gordon-app2.example.com",
		Image: "apache:latest",
		Ports: []int{80},
	}
	mockRuntime.EXPECT().InspectContainer(ctx, "stopped-container").Return(startedContainer, nil)

	err = manager.AutoStartContainers(ctx)

	assert.NoError(t, err)
	assert.Len(t, manager.containers, 2)
	assert.Contains(t, manager.containers, "app1.example.com")
	assert.Contains(t, manager.containers, "app2.example.com")
}

func TestManager_AutoStartContainers_DeployNew(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
		},
		Routes: map[string]string{
			"new-app.example.com": "redis:latest",
		},
		Volumes: config.VolumeConfig{
			AutoCreate: false,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled: false,
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// No existing containers
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)

	// Should deploy new container
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{"redis:latest"}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "redis:latest").Return([]int{6379}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "redis:latest").Return([]string{}, nil)

	newContainer := &runtime.Container{
		ID:    "new-container",
		Name:  "gordon-new-app.example.com",
		Image: "redis:latest",
		Ports: []int{6379},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(newContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "new-container").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "new-container").Return(newContainer, nil)

	err = manager.AutoStartContainers(ctx)

	assert.NoError(t, err)
	assert.Len(t, manager.containers, 1)
	assert.Contains(t, manager.containers, "new-app.example.com")
}

func TestManager_AutoStartContainers_CleanupConflicts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
		},
		Routes: map[string]string{
			"app.example.com": "nginx:latest",
		},
		Volumes: config.VolumeConfig{
			AutoCreate: false,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled: false,
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Existing containers include a non-Gordon container with same image
	existingContainers := []*runtime.Container{
		{
			ID:    "unmanaged-container",
			Name:  "some-nginx-container",
			Image: "nginx:latest",
			// No Gordon labels - not managed
		},
	}

	mockRuntime.EXPECT().ListContainers(ctx, true).Return(existingContainers, nil)

	// Should stop and remove the conflicting container
	mockRuntime.EXPECT().IsContainerRunning(ctx, "unmanaged-container").Return(true, nil)
	mockRuntime.EXPECT().StopContainer(ctx, "unmanaged-container").Return(nil)
	mockRuntime.EXPECT().RemoveContainer(ctx, "unmanaged-container", true).Return(nil)

	// Then deploy new Gordon-managed container
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{"nginx:latest"}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "nginx:latest").Return([]int{80}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "nginx:latest").Return([]string{}, nil)

	newContainer := &runtime.Container{
		ID:    "gordon-container",
		Name:  "gordon-app.example.com",
		Image: "nginx:latest",
		Ports: []int{80},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(newContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "gordon-container").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "gordon-container").Return(newContainer, nil)

	err = manager.AutoStartContainers(ctx)

	assert.NoError(t, err)
	assert.Len(t, manager.containers, 1)
	assert.Contains(t, manager.containers, "app.example.com")
}

func TestManager_AutoStartContainers_NoRoutes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
		},
		// No routes configured
		Routes: map[string]string{},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	err = manager.AutoStartContainers(ctx)

	assert.NoError(t, err)
	assert.Empty(t, manager.containers)
}

func TestManager_normalizeImageName(t *testing.T) {
	manager := &Manager{}

	tests := []struct {
		input    string
		expected string
	}{
		{"nginx", "docker.io/library/nginx"},
		{"nginx:latest", "docker.io/library/nginx"},
		{"user/repo", "docker.io/user/repo"},
		{"user/repo:tag", "docker.io/user/repo"},
		{"registry.com/user/repo", "registry.com/user/repo"},
		{"registry.com/user/repo:tag", "registry.com/user/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := manager.normalizeImageName(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}