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

func TestManager_cleanupVolumesForDomain(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Prefix: "gordon",
		},
		Routes: map[string]string{
			"test.example.com": "nginx:latest",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Mock image inspection for volume paths
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "nginx:latest").Return([]string{"/var/log/nginx", "/etc/nginx"}, nil)

	// Mock volume existence checks and removal
	varLogNginxVolume := generateVolumeName("gordon", "test.example.com", "/var/log/nginx")
	etcNginxVolume := generateVolumeName("gordon", "test.example.com", "/etc/nginx")
	
	mockRuntime.EXPECT().VolumeExists(ctx, varLogNginxVolume).Return(true, nil)
	mockRuntime.EXPECT().RemoveVolume(ctx, varLogNginxVolume, true).Return(nil)

	mockRuntime.EXPECT().VolumeExists(ctx, etcNginxVolume).Return(true, nil)
	mockRuntime.EXPECT().RemoveVolume(ctx, etcNginxVolume, true).Return(nil)

	err = manager.cleanupVolumesForDomain(ctx, "test.example.com")

	assert.NoError(t, err)
}

func TestManager_cleanupVolumesForDomain_NoRoute(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Prefix: "gordon",
		},
		Routes: map[string]string{},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Should return nil without doing anything
	err = manager.cleanupVolumesForDomain(ctx, "nonexistent.example.com")

	assert.NoError(t, err)
}

func TestManager_cleanupVolumesForDomain_WithRegistryAuth(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			RegistryDomain: "registry.example.com",
		},
		RegistryAuth: config.RegistryAuthConfig{
			Enabled: true,
		},
		Volumes: config.VolumeConfig{
			Prefix: "gordon",
		},
		Routes: map[string]string{
			"test.example.com": "myapp:latest",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Should use prefixed image reference
	expectedImageRef := "registry.example.com/myapp:latest"
	mockRuntime.EXPECT().InspectImageVolumes(ctx, expectedImageRef).Return([]string{"/data"}, nil)

	dataVolume := generateVolumeName("gordon", "test.example.com", "/data")
	mockRuntime.EXPECT().VolumeExists(ctx, dataVolume).Return(true, nil)
	mockRuntime.EXPECT().RemoveVolume(ctx, dataVolume, true).Return(nil)

	err = manager.cleanupVolumesForDomain(ctx, "test.example.com")

	assert.NoError(t, err)
}

func TestManager_cleanupVolumesForDomain_NoVolumes(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Prefix: "gordon",
		},
		Routes: map[string]string{
			"test.example.com": "alpine:latest",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Image has no volumes
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "alpine:latest").Return([]string{}, nil)

	err = manager.cleanupVolumesForDomain(ctx, "test.example.com")

	assert.NoError(t, err)
}

func TestManager_cleanupVolumesForDomain_VolumeNotExists(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Prefix: "gordon",
		},
		Routes: map[string]string{
			"test.example.com": "nginx:latest",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	mockRuntime.EXPECT().InspectImageVolumes(ctx, "nginx:latest").Return([]string{"/var/log/nginx"}, nil)

	// Volume doesn't exist, should skip removal
	varLogNginxVolume := generateVolumeName("gordon", "test.example.com", "/var/log/nginx")
	mockRuntime.EXPECT().VolumeExists(ctx, varLogNginxVolume).Return(false, nil)

	err = manager.cleanupVolumesForDomain(ctx, "test.example.com")

	assert.NoError(t, err)
}

func TestManager_cleanupVolumesForDomain_Errors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Volumes: config.VolumeConfig{
			Prefix: "gordon",
		},
		Routes: map[string]string{
			"test.example.com": "nginx:latest",
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	// Test error inspecting image volumes
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "nginx:latest").Return(nil, errors.New("inspect failed"))

	err = manager.cleanupVolumesForDomain(ctx, "test.example.com")
	assert.Error(t, err)

	// Test error checking volume existence
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "nginx:latest").Return([]string{"/var/log/nginx"}, nil)
	varLogNginxVolume := generateVolumeName("gordon", "test.example.com", "/var/log/nginx")
	mockRuntime.EXPECT().VolumeExists(ctx, varLogNginxVolume).Return(false, errors.New("check failed"))

	err = manager.cleanupVolumesForDomain(ctx, "test.example.com")
	assert.NoError(t, err) // Should continue despite check error

	// Test error removing volume
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "nginx:latest").Return([]string{"/var/log/nginx"}, nil)
	mockRuntime.EXPECT().VolumeExists(ctx, varLogNginxVolume).Return(true, nil)
	mockRuntime.EXPECT().RemoveVolume(ctx, varLogNginxVolume, true).Return(errors.New("remove failed"))

	err = manager.cleanupVolumesForDomain(ctx, "test.example.com")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to cleanup 1 volumes")
}

func TestManager_DeployContainer_VolumeAutoCreation(t *testing.T) {
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
			Enabled: false,
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	route := config.Route{
		Domain: "test.example.com",
		Image:  "postgres:latest",
	}

	// Set up expectations for deployment with volume creation
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{"postgres:latest"}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "postgres:latest").Return([]int{5432}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "postgres:latest").Return([]string{}, nil)

	// Mock volume discovery and creation
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "postgres:latest").Return([]string{"/var/lib/postgresql/data", "/etc/postgresql"}, nil)

	// First volume
	dataVolume := generateVolumeName("gordon", "test.example.com", "/var/lib/postgresql/data")
	mockRuntime.EXPECT().VolumeExists(ctx, dataVolume).Return(false, nil)
	mockRuntime.EXPECT().CreateVolume(ctx, dataVolume).Return(nil)

	// Second volume already exists
	etcVolume := generateVolumeName("gordon", "test.example.com", "/etc/postgresql")
	mockRuntime.EXPECT().VolumeExists(ctx, etcVolume).Return(true, nil)

	expectedContainer := &runtime.Container{
		ID:     "postgres-container",
		Name:   "gordon-test.example.com",
		Image:  "postgres:latest",
		Status: "running",
		Ports:  []int{5432},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "postgres-container").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "postgres-container").Return(expectedContainer, nil)

	container, err := manager.DeployContainer(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, expectedContainer, container)
}

func TestManager_DeployContainer_VolumeCreationError(t *testing.T) {
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
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled: false,
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	route := config.Route{
		Domain: "test.example.com",
		Image:  "postgres:latest",
	}

	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{"postgres:latest"}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "postgres:latest").Return([]int{5432}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "postgres:latest").Return([]string{}, nil)

	// Mock volume creation failure
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "postgres:latest").Return([]string{"/var/lib/postgresql/data"}, nil)
	dataVolume := generateVolumeName("gordon", "test.example.com", "/var/lib/postgresql/data")
	mockRuntime.EXPECT().VolumeExists(ctx, dataVolume).Return(false, nil)
	mockRuntime.EXPECT().CreateVolume(ctx, dataVolume).Return(errors.New("create failed"))

	// Should still continue with container creation (volume creation is best-effort)
	expectedContainer := &runtime.Container{
		ID:     "postgres-container",
		Name:   "gordon-test.example.com",
		Image:  "postgres:latest",
		Status: "running",
		Ports:  []int{5432},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "postgres-container").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "postgres-container").Return(expectedContainer, nil)

	container, err := manager.DeployContainer(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, expectedContainer, container)
}

func TestManager_DeployContainer_VolumeAutoCreationDisabled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
		},
		Volumes: config.VolumeConfig{
			AutoCreate: false, // Disabled
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled: false,
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	route := config.Route{
		Domain: "test.example.com",
		Image:  "postgres:latest",
	}

	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{"postgres:latest"}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "postgres:latest").Return([]int{5432}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "postgres:latest").Return([]string{}, nil)

	// Should NOT inspect image volumes when auto-creation is disabled

	expectedContainer := &runtime.Container{
		ID:     "postgres-container",
		Name:   "gordon-test.example.com",
		Image:  "postgres:latest",
		Status: "running",
		Ports:  []int{5432},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "postgres-container").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "postgres-container").Return(expectedContainer, nil)

	container, err := manager.DeployContainer(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, expectedContainer, container)
}