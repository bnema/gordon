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

func TestManager_DeployContainer_ImagePull(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
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

	route := config.Route{
		Domain: "test.example.com",
		Image:  "alpine:latest",
	}

	// Image not found locally, should pull
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{}, nil)
	mockRuntime.EXPECT().PullImage(ctx, "alpine:latest").Return(nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "alpine:latest").Return([]int{}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "alpine:latest").Return([]string{}, nil)

	expectedContainer := &runtime.Container{
		ID:     "container456",
		Name:   "gordon-test.example.com",
		Image:  "alpine:latest",
		Status: "running",
		Ports:  []int{},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "container456").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "container456").Return(expectedContainer, nil)

	container, err := manager.DeployContainer(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, expectedContainer, container)
}

func TestManager_DeployContainer_WithAuth(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime:        "docker",
			RegistryDomain: "registry.example.com",
		},
		RegistryAuth: config.RegistryAuthConfig{
			Enabled:  true,
			Username: "testuser",
			Password: "testpass",
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

	route := config.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// Should use authenticated pull with registry domain prefix
	expectedImageRef := "registry.example.com/myapp:latest"
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{}, nil)
	mockRuntime.EXPECT().PullImageWithAuth(ctx, expectedImageRef, "testuser", "testpass").Return(nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, expectedImageRef).Return([]int{8080}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, expectedImageRef).Return([]string{}, nil)

	expectedContainer := &runtime.Container{
		ID:     "container789",
		Name:   "gordon-test.example.com",
		Image:  expectedImageRef,
		Status: "running",
		Ports:  []int{8080},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "container789").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "container789").Return(expectedContainer, nil)

	container, err := manager.DeployContainer(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, expectedContainer, container)
}

func TestManager_DeployContainer_ReplaceExisting(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
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

	// Add existing container
	existingContainer := &runtime.Container{
		ID:     "old-container",
		Name:   "gordon-test.example.com",
		Status: "running",
	}
	manager.containers["test.example.com"] = existingContainer

	route := config.Route{
		Domain: "test.example.com",
		Image:  "nginx:latest",
	}

	// Should stop and remove existing container
	mockRuntime.EXPECT().StopContainer(ctx, "old-container").Return(nil)
	mockRuntime.EXPECT().RemoveContainer(ctx, "old-container", true).Return(nil)

	// Check for orphaned containers
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{"nginx:latest"}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "nginx:latest").Return([]int{80}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "nginx:latest").Return([]string{}, nil)

	newContainer := &runtime.Container{
		ID:     "new-container",
		Name:   "gordon-test.example.com",
		Image:  "nginx:latest",
		Status: "running",
		Ports:  []int{80},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(newContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "new-container").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "new-container").Return(newContainer, nil)

	container, err := manager.DeployContainer(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, newContainer, container)
	assert.Equal(t, newContainer, manager.containers["test.example.com"])
}

func TestManager_DeployContainer_Errors(t *testing.T) {
	tests := []struct {
		name          string
		setupMocks    func(*mocks.MockRuntime)
		expectedError string
	}{
		{
			name: "pull image error",
			setupMocks: func(m *mocks.MockRuntime) {
				m.EXPECT().ListContainers(gomock.Any(), true).Return([]*runtime.Container{}, nil)
				m.EXPECT().ListImages(gomock.Any()).Return([]string{}, nil)
				m.EXPECT().PullImage(gomock.Any(), "nginx:latest").Return(errors.New("pull failed"))
			},
			expectedError: "failed to pull image",
		},
		{
			name: "create container error",
			setupMocks: func(m *mocks.MockRuntime) {
				m.EXPECT().ListContainers(gomock.Any(), true).Return([]*runtime.Container{}, nil)
				m.EXPECT().ListImages(gomock.Any()).Return([]string{"nginx:latest"}, nil)
				m.EXPECT().GetImageExposedPorts(gomock.Any(), "nginx:latest").Return([]int{80}, nil)
				m.EXPECT().InspectImageEnv(gomock.Any(), "nginx:latest").Return([]string{}, nil)
				m.EXPECT().CreateContainer(gomock.Any(), gomock.Any()).Return(nil, errors.New("create failed"))
			},
			expectedError: "failed to create container",
		},
		{
			name: "start container error",
			setupMocks: func(m *mocks.MockRuntime) {
				m.EXPECT().ListContainers(gomock.Any(), true).Return([]*runtime.Container{}, nil)
				m.EXPECT().ListImages(gomock.Any()).Return([]string{"nginx:latest"}, nil)
				m.EXPECT().GetImageExposedPorts(gomock.Any(), "nginx:latest").Return([]int{80}, nil)
				m.EXPECT().InspectImageEnv(gomock.Any(), "nginx:latest").Return([]string{}, nil)
				container := &runtime.Container{ID: "test123"}
				m.EXPECT().CreateContainer(gomock.Any(), gomock.Any()).Return(container, nil)
				m.EXPECT().StartContainer(gomock.Any(), "test123").Return(errors.New("start failed"))
				m.EXPECT().RemoveContainer(gomock.Any(), "test123", true).Return(nil)
			},
			expectedError: "failed to start container",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx := testutils.TestContext(t)
			mockRuntime := mocks.NewMockRuntime(ctrl)

			cfg := &config.Config{
				Server: config.ServerConfig{
					Runtime: "docker",
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

			route := config.Route{
				Domain: "test.example.com",
				Image:  "nginx:latest",
			}

			tt.setupMocks(mockRuntime)

			_, err = manager.DeployContainer(ctx, route)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.expectedError)
		})
	}
}

func TestManager_DeployContainer_WithNetworkAttachments(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx := testutils.TestContext(t)
	mockRuntime := mocks.NewMockRuntime(ctrl)

	cfg := &config.Config{
		Server: config.ServerConfig{
			Runtime: "docker",
		},
		Volumes: config.VolumeConfig{
			AutoCreate: false,
		},
		NetworkIsolation: config.NetworkIsolationConfig{
			Enabled:       true,
			NetworkPrefix: "gordon",
		},
		NetworkGroups: map[string][]string{
			"backend": {"api.example.com", "worker.example.com"},
		},
		Attachments: map[string][]string{
			"api.example.com": {"redis:latest"},
			"backend":         {"postgres:latest"},
		},
	}

	manager, err := NewManagerWithRuntime(cfg, mockRuntime)
	require.NoError(t, err)

	route := config.Route{
		Domain: "api.example.com",
		Image:  "myapi:latest",
	}

	// Main container deployment expectations
	mockRuntime.EXPECT().ListContainers(ctx, true).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().ListImages(ctx).Return([]string{"myapi:latest"}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "myapi:latest").Return([]int{8080}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "myapi:latest").Return([]string{}, nil)
	mockRuntime.EXPECT().NetworkExists(ctx, "gordon-backend").Return(true, nil)

	expectedContainer := &runtime.Container{
		ID:     "main-container",
		Name:   "gordon-api.example.com",
		Image:  "myapi:latest",
		Status: "running",
		Ports:  []int{8080},
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "main-container").Return(nil)
	mockRuntime.EXPECT().InspectContainer(ctx, "main-container").Return(expectedContainer, nil)

	// Attachment deployment expectations for app-specific service
	mockRuntime.EXPECT().ListContainers(ctx, false).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "redis:latest").Return([]int{6379}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "redis:latest").Return([]string{}, nil)

	redisContainer := &runtime.Container{
		ID:   "redis-container",
		Name: "api-example-com-redis",
	}
	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(redisContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "redis-container").Return(nil)

	// Attachment deployment expectations for network group service
	mockRuntime.EXPECT().ListContainers(ctx, false).Return([]*runtime.Container{redisContainer}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "postgres:latest").Return([]int{5432}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "postgres:latest").Return([]string{}, nil)

	pgContainer := &runtime.Container{
		ID:   "pg-container",
		Name: "gordon-shared-postgres",
	}
	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(pgContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "pg-container").Return(nil)

	container, err := manager.DeployContainer(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, expectedContainer, container)
}

func TestManager_DeployAttachedService(t *testing.T) {
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

	// Test deploying redis service for an app
	mockRuntime.EXPECT().ListContainers(ctx, false).Return([]*runtime.Container{}, nil)
	mockRuntime.EXPECT().GetImageExposedPorts(ctx, "redis:latest").Return([]int{6379}, nil)
	mockRuntime.EXPECT().InspectImageEnv(ctx, "redis:latest").Return([]string{"REDIS_VERSION=7.0"}, nil)
	mockRuntime.EXPECT().InspectImageVolumes(ctx, "redis:latest").Return([]string{"/data"}, nil)
	mockRuntime.EXPECT().VolumeExists(ctx, "gordon-test-example-com-data").Return(false, nil)
	mockRuntime.EXPECT().CreateVolume(ctx, "gordon-test-example-com-data").Return(nil)

	expectedContainer := &runtime.Container{
		ID:   "redis-service",
		Name: "test-example-com-redis",
	}

	mockRuntime.EXPECT().CreateContainer(ctx, gomock.Any()).Return(expectedContainer, nil)
	mockRuntime.EXPECT().StartContainer(ctx, "redis-service").Return(nil)

	err = manager.DeployAttachedService(ctx, "test.example.com", "redis:latest")

	assert.NoError(t, err)
}