package container

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"gordon/internal/config"
	"gordon/internal/testutils"
	"gordon/internal/testutils/mocks"
	"gordon/pkg/runtime"
)

func TestManager_SyncContainers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Add some containers to internal state that might be stale
	manager.containers["old.example.com"] = &runtime.Container{ID: "old-container"}

	// Mock runtime returning current containers
	currentContainers := []*runtime.Container{
		{
			ID:   "active-container-1",
			Name: "gordon-app1.example.com",
			Labels: map[string]string{
				"gordon.managed": "true",
				"gordon.domain":  "app1.example.com",
			},
		},
		{
			ID:   "active-container-2",
			Name: "gordon-app2.example.com",
			Labels: map[string]string{
				"gordon.managed": "true",
				"gordon.domain":  "app2.example.com",
			},
		},
		{
			ID:   "unmanaged-container",
			Name: "some-other-container",
			Labels: map[string]string{
				"other.label": "value",
			},
		},
	}

	mockRuntime.EXPECT().ListContainers(ctx, false).Return(currentContainers, nil)

	err = manager.SyncContainers(ctx)

	assert.NoError(t, err)
	assert.Len(t, manager.containers, 2)
	assert.Contains(t, manager.containers, "app1.example.com")
	assert.Contains(t, manager.containers, "app2.example.com")
	assert.NotContains(t, manager.containers, "old.example.com")
	assert.Equal(t, "active-container-1", manager.containers["app1.example.com"].ID)
	assert.Equal(t, "active-container-2", manager.containers["app2.example.com"].ID)
}

func TestManager_SyncContainers_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	mockRuntime.EXPECT().ListContainers(ctx, false).Return(nil, errors.New("runtime error"))

	err = manager.SyncContainers(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list containers")
}

func TestManager_StopContainer(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	mockRuntime.EXPECT().StopContainer(ctx, "container123").Return(nil)

	err = manager.StopContainer(ctx, "container123")

	assert.NoError(t, err)
}

func TestManager_StopContainer_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	mockRuntime.EXPECT().StopContainer(ctx, "container123").Return(errors.New("stop failed"))

	err = manager.StopContainer(ctx, "container123")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to stop container")
}

func TestManager_StopContainerByDomain(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	container := &runtime.Container{
		ID: "container123",
	}
	manager.containers["test.example.com"] = container

	mockRuntime.EXPECT().StopContainer(ctx, "container123").Return(nil)

	err = manager.StopContainerByDomain(ctx, "test.example.com")

	assert.NoError(t, err)
}

func TestManager_StopContainerByDomain_NotFound(t *testing.T) {
	cfg := &config.Config{}
	manager := &Manager{
		config:     cfg,
		containers: make(map[string]*runtime.Container),
	}

	ctx := testutils.TestContext(t)
	err := manager.StopContainerByDomain(ctx, "nonexistent.example.com")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no container found for domain")
}

func TestManager_RemoveContainer(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Preserve: true, // Don't cleanup volumes
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled: false,
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	container := &runtime.Container{
		ID: "container123",
	}
	manager.containers["test.example.com"] = container

	mockRuntime.EXPECT().RemoveContainer(ctx, "container123", true).Return(nil)

	err = manager.RemoveContainer(ctx, "container123", true)

	assert.NoError(t, err)
	assert.NotContains(t, manager.containers, "test.example.com")
}

func TestManager_RemoveContainer_WithVolumeCleanup(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Preserve: false, // Should cleanup volumes
			Prefix:   "gordon",
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled: false,
		},
		Routes: map[string]string{
			"test.example.com": "nginx:latest",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	container := &runtime.Container{
		ID: "container123",
	}
	manager.containers["test.example.com"] = container

	mockRuntime.EXPECT().RemoveContainer(ctx, "container123", true).Return(nil)

	// Expect volume cleanup
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "nginx:latest").Return([]string{"/var/log/nginx"}, nil)
	varLogNginxVolume := generateVolumeName("gordon", "test.example.com", "/var/log/nginx")
	mockRuntime.EXPECT().VolumeExists(ctx, varLogNginxVolume).Return(true, nil)
	mockRuntime.EXPECT().RemoveVolume(ctx, varLogNginxVolume, true).Return(nil)

	err = manager.RemoveContainer(ctx, "container123", true)

	assert.NoError(t, err)
	assert.NotContains(t, manager.containers, "test.example.com")
}

func TestManager_RemoveContainer_WithNetworkCleanup(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Preserve: true,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled:       true,
			NetworkPrefix: "gordon",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	container := &runtime.Container{
		ID: "container123",
	}
	manager.containers["test.example.com"] = container

	mockRuntime.EXPECT().RemoveContainer(ctx, "container123", true).Return(nil)

	// Expect network cleanup check
	mockRuntime.EXPECT().ListNetworks(ctx).Return([]*runtime.NetworkInfo{
		{
			Name:       "gordon-test-example-com",
			Containers: []string{}, // Empty network
		},
	}, nil)
	mockRuntime.EXPECT().RemoveNetwork(ctx, "gordon-test-example-com").Return(nil)

	err = manager.RemoveContainer(ctx, "container123", true)

	assert.NoError(t, err)
	assert.NotContains(t, manager.containers, "test.example.com")
}

func TestManager_RemoveContainerByDomain(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Preserve: true,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled: false,
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	container := &runtime.Container{
		ID: "container123",
	}
	manager.containers["test.example.com"] = container

	mockRuntime.EXPECT().RemoveContainer(ctx, "container123", false).Return(nil)

	err = manager.RemoveContainerByDomain(ctx, "test.example.com", false)

	assert.NoError(t, err)
	assert.NotContains(t, manager.containers, "test.example.com")
}

func TestManager_RemoveContainerByDomain_NotFound(t *testing.T) {
	cfg := &config.Config{}
	manager := &Manager{
		config:     cfg,
		containers: make(map[string]*runtime.Container),
	}

	ctx := testutils.TestContext(t)
	err := manager.RemoveContainerByDomain(ctx, "nonexistent.example.com", false)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no container found for domain")
}

func TestManager_GetContainerPort(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	container := &runtime.Container{
		ID: "container123",
	}
	manager.containers["test.example.com"] = container

	mockRuntime.EXPECT().GetContainerPort(ctx, "container123", 80).Return(8080, nil)

	port, err := manager.GetContainerPort(ctx, "test.example.com", 80)

	assert.NoError(t, err)
	assert.Equal(t, 8080, port)
}

func TestManager_GetContainerPort_NotFound(t *testing.T) {
	cfg := &config.Config{}
	manager := &Manager{
		config:     cfg,
		containers: make(map[string]*runtime.Container),
	}

	ctx := testutils.TestContext(t)
	_, err := manager.GetContainerPort(ctx, "nonexistent.example.com", 80)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no container found for domain")
}

func TestManager_FindContainerByDomain(t *testing.T) {
	manager := &Manager{
		containers: map[string]*runtime.Container{
			"test.example.com": {ID: "container123"},
		},
	}

	id, exists := manager.FindContainerByDomain("test.example.com")
	assert.True(t, exists)
	assert.Equal(t, "container123", id)

	id, exists = manager.FindContainerByDomain("nonexistent.example.com")
	assert.False(t, exists)
	assert.Empty(t, id)
}

func TestManager_FindDomainByContainerID(t *testing.T) {
	manager := &Manager{
		containers: map[string]*runtime.Container{
			"test.example.com": {ID: "container123"},
			"app.example.com":  {ID: "container456"},
		},
	}

	domain, exists := manager.FindDomainByContainerID("container123")
	assert.True(t, exists)
	assert.Equal(t, "test.example.com", domain)

	domain, exists = manager.FindDomainByContainerID("nonexistent")
	assert.False(t, exists)
	assert.Empty(t, domain)
}

func TestManager_StopAllManagedContainers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	manager.containers["app1.example.com"] = &runtime.Container{ID: "container1"}
	manager.containers["app2.example.com"] = &runtime.Container{ID: "container2"}

	mockRuntime.EXPECT().StopContainer(ctx, "container1").Return(nil)
	mockRuntime.EXPECT().StopContainer(ctx, "container2").Return(nil)

	err = manager.StopAllManagedContainers(ctx)

	assert.NoError(t, err)
	assert.Empty(t, manager.containers)
}

func TestManager_StopAllManagedContainers_WithErrors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	manager.containers["app1.example.com"] = &runtime.Container{ID: "container1"}
	manager.containers["app2.example.com"] = &runtime.Container{ID: "container2"}

	mockRuntime.EXPECT().StopContainer(ctx, "container1").Return(nil)
	mockRuntime.EXPECT().StopContainer(ctx, "container2").Return(errors.New("stop failed"))

	err = manager.StopAllManagedContainers(ctx)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to stop 1 containers")
}

func TestManager_StopAllManagedContainers_Empty(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	err = manager.StopAllManagedContainers(ctx)

	assert.NoError(t, err)
	assert.Empty(t, manager.containers)
}

func TestManager_UpdateConfig(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := mocks.NewMockRuntime(ctrl)

	originalCfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
		},
	}

	manager, err := NewManagerWithRuntime(originalCfg, mockRuntime)
	require.NoError(t, err)

	newCfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "podman",
		},
		Routes: map[string]string{
			"new-app.example.com": "nginx:latest",
		},
	}

	manager.UpdateConfig(newCfg)

	// Cleanup the env file created by UpdateConfig
	defer func() {
		if err := os.Remove("new-app_example_com.env"); err != nil && !os.IsNotExist(err) {
			t.Logf("Failed to cleanup env file: %v", err)
		}
	}()

	assert.Equal(t, newCfg, manager.config)
	assert.Equal(t, "podman", manager.config.Server.Runtime)
}

func TestManager_Shutdown(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{}
	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	manager.containers["app1.example.com"] = &runtime.Container{ID: "container1"}

	mockRuntime.EXPECT().StopContainer(ctx, "container1").Return(nil)

	err = manager.Shutdown(ctx)

	assert.NoError(t, err)
	assert.Empty(t, manager.containers)
}