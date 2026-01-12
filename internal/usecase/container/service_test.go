package container

import (
	"context"
	"errors"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"gordon/internal/boundaries/out/mocks"
	"gordon/internal/domain"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func TestService_Deploy_Success(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		NetworkIsolation: false,
		VolumeAutoCreate: false,
	}

	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// Setup mocks - no orphaned containers
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)

	// Image is not found locally, needs to be pulled
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:latest").Return(nil)

	// Get exposed ports
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil)

	// Load environment
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{"FOO=bar"}, nil)

	// Inspect image env
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return([]string{"DEFAULT=value"}, nil)

	// No volume auto-create, so no volume calls

	// Create and start container
	createdContainer := &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Image:  "myapp:latest",
		Status: "created",
	}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(createdContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "container-123").Return(nil)

	// Re-inspect after start
	runningContainer := &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Image:  "myapp:latest",
		Status: "running",
		Ports:  []int{8080},
	}
	runtime.EXPECT().InspectContainer(mock.Anything, "container-123").Return(runningContainer, nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "container-123", result.ID)
	assert.Equal(t, "running", result.Status)

	// Verify container is tracked
	tracked, exists := svc.Get(ctx, "test.example.com")
	assert.True(t, exists)
	assert.Equal(t, "container-123", tracked.ID)
}

func TestService_Deploy_ImagePullFailure(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:latest").Return(errors.New("image not found"))

	result, err := svc.Deploy(ctx, route)

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to pull image")
}

func TestService_Deploy_ReplacesExistingContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	// Pre-populate with existing container
	existingContainer := &domain.Container{
		ID:     "old-container",
		Name:   "gordon-test.example.com",
		Status: "running",
	}
	svc.containers["test.example.com"] = existingContainer

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:v2",
	}

	// Stop and remove existing container
	runtime.EXPECT().StopContainer(mock.Anything, "old-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "old-container", true).Return(nil)

	// Cleanup orphans
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)

	// Image operations
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:v2").Return(nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:v2").Return([]int{8080}, nil)

	// Environment
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:v2").Return([]string{}, nil)

	// Create new container
	newContainer := &domain.Container{ID: "new-container", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.Anything).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID:     "new-container",
		Status: "running",
	}, nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)

	// Old container should be replaced
	tracked, exists := svc.Get(ctx, "test.example.com")
	assert.True(t, exists)
	assert.Equal(t, "new-container", tracked.ID)
}

func TestService_Deploy_WithNetworkIsolation(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		NetworkIsolation: true,
		NetworkPrefix:    "gordon",
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:latest"}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return([]string{}, nil)

	// Network isolation - should check and create network
	runtime.EXPECT().NetworkExists(mock.Anything, "gordon-test-example-com").Return(false, nil)
	runtime.EXPECT().CreateNetwork(mock.Anything, "gordon-test-example-com", map[string]string{"driver": "bridge"}).Return(nil)

	container := &domain.Container{ID: "container-123", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.NetworkMode == "gordon-test-example-com"
	})).Return(container, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "container-123").Return(nil)
	runtime.EXPECT().InspectContainer(mock.Anything, "container-123").Return(&domain.Container{
		ID:     "container-123",
		Status: "running",
	}, nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestService_Deploy_WithVolumeAutoCreate(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		VolumeAutoCreate: true,
		VolumePrefix:     "gordon",
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:latest"}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return([]string{}, nil)

	// Volume operations
	runtime.EXPECT().InspectImageVolumes(mock.Anything, "myapp:latest").Return([]string{"/data", "/config"}, nil)
	runtime.EXPECT().VolumeExists(mock.Anything, "gordon-test-example-com-data").Return(false, nil)
	runtime.EXPECT().CreateVolume(mock.Anything, "gordon-test-example-com-data").Return(nil)
	runtime.EXPECT().VolumeExists(mock.Anything, "gordon-test-example-com-config").Return(true, nil)

	container := &domain.Container{ID: "container-123", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return len(cfg.Volumes) == 2
	})).Return(container, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "container-123").Return(nil)
	runtime.EXPECT().InspectContainer(mock.Anything, "container-123").Return(&domain.Container{
		ID:     "container-123",
		Status: "running",
	}, nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestService_Stop(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	runtime.EXPECT().StopContainer(mock.Anything, "container-123").Return(nil)

	err := svc.Stop(ctx, "container-123")

	assert.NoError(t, err)
}

func TestService_Stop_Error(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	runtime.EXPECT().StopContainer(mock.Anything, "container-123").Return(errors.New("container not found"))

	err := svc.Stop(ctx, "container-123")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to stop container")
}

func TestService_Remove(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	// Add tracked container
	svc.containers["test.example.com"] = &domain.Container{
		ID:   "container-123",
		Name: "gordon-test.example.com",
	}

	runtime.EXPECT().RemoveContainer(mock.Anything, "container-123", true).Return(nil)

	err := svc.Remove(ctx, "container-123", true)

	assert.NoError(t, err)

	// Verify container is no longer tracked
	_, exists := svc.Get(ctx, "test.example.com")
	assert.False(t, exists)
}

func TestService_Remove_WithNetworkCleanup(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		NetworkIsolation: true,
		NetworkPrefix:    "gordon",
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	svc.containers["test.example.com"] = &domain.Container{
		ID:   "container-123",
		Name: "gordon-test.example.com",
	}

	runtime.EXPECT().RemoveContainer(mock.Anything, "container-123", true).Return(nil)
	runtime.EXPECT().ListNetworks(mock.Anything).Return([]*domain.NetworkInfo{
		{Name: "gordon-test-example-com", Containers: []string{}},
	}, nil)
	runtime.EXPECT().RemoveNetwork(mock.Anything, "gordon-test-example-com").Return(nil)

	err := svc.Remove(ctx, "container-123", true)

	assert.NoError(t, err)
}

func TestService_Get(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	container := &domain.Container{ID: "container-123"}
	svc.containers["test.example.com"] = container

	result, exists := svc.Get(ctx, "test.example.com")

	assert.True(t, exists)
	assert.Equal(t, "container-123", result.ID)
}

func TestService_Get_NotFound(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	result, exists := svc.Get(ctx, "nonexistent.example.com")

	assert.False(t, exists)
	assert.Nil(t, result)
}

func TestService_List(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["app1.example.com"] = &domain.Container{ID: "container-1"}
	svc.containers["app2.example.com"] = &domain.Container{ID: "container-2"}

	result := svc.List(ctx)

	assert.Len(t, result, 2)
	assert.Equal(t, "container-1", result["app1.example.com"].ID)
	assert.Equal(t, "container-2", result["app2.example.com"].ID)
}

func TestService_HealthCheck(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["healthy.example.com"] = &domain.Container{ID: "healthy-container"}
	svc.containers["unhealthy.example.com"] = &domain.Container{ID: "unhealthy-container"}

	runtime.EXPECT().IsContainerRunning(mock.Anything, "healthy-container").Return(true, nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "unhealthy-container").Return(false, nil)

	result := svc.HealthCheck(ctx)

	assert.True(t, result["healthy.example.com"])
	assert.False(t, result["unhealthy.example.com"])
}

func TestService_SyncContainers(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	runtime.EXPECT().ListContainers(mock.Anything, false).Return([]*domain.Container{
		{
			ID:   "container-1",
			Name: "gordon-app1.example.com",
			Labels: map[string]string{
				"gordon.domain":  "app1.example.com",
				"gordon.managed": "true",
			},
		},
		{
			ID:   "container-2",
			Name: "gordon-app2.example.com",
			Labels: map[string]string{
				"gordon.domain":  "app2.example.com",
				"gordon.managed": "true",
			},
		},
		{
			ID:     "unmanaged-container",
			Name:   "some-other-app",
			Labels: map[string]string{},
		},
	}, nil)

	err := svc.SyncContainers(ctx)

	assert.NoError(t, err)
	assert.Len(t, svc.containers, 2)
	assert.Contains(t, svc.containers, "app1.example.com")
	assert.Contains(t, svc.containers, "app2.example.com")
}

func TestService_Shutdown(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["app1.example.com"] = &domain.Container{ID: "container-1"}
	svc.containers["app2.example.com"] = &domain.Container{ID: "container-2"}

	runtime.EXPECT().StopContainer(mock.Anything, "container-1").Return(nil)
	runtime.EXPECT().StopContainer(mock.Anything, "container-2").Return(nil)

	err := svc.Shutdown(ctx)

	assert.NoError(t, err)
	assert.Empty(t, svc.containers)
}

func TestService_Shutdown_PartialFailure(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["app1.example.com"] = &domain.Container{ID: "container-1"}
	svc.containers["app2.example.com"] = &domain.Container{ID: "container-2"}

	runtime.EXPECT().StopContainer(mock.Anything, "container-1").Return(nil)
	runtime.EXPECT().StopContainer(mock.Anything, "container-2").Return(errors.New("stop failed"))

	err := svc.Shutdown(ctx)

	// Shutdown logs errors but always returns nil for graceful degradation
	assert.NoError(t, err)
	// One container should still be tracked (the one that failed to stop)
	assert.Len(t, svc.containers, 1)
}

func TestNormalizeImageRef(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple image",
			input:    "nginx",
			expected: "docker.io/library/nginx",
		},
		{
			name:     "user/repo",
			input:    "user/myapp",
			expected: "docker.io/user/myapp",
		},
		{
			name:     "registry/repo",
			input:    "gcr.io/project/image",
			expected: "gcr.io/project/image",
		},
		{
			name:     "with tag",
			input:    "nginx:latest",
			expected: "docker.io/library/nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeImageRef(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGenerateVolumeName(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		domain     string
		volumePath string
		expected   string
	}{
		{
			name:       "simple path",
			prefix:     "gordon",
			domain:     "app.example.com",
			volumePath: "/data",
			expected:   "gordon-app-example-com-data",
		},
		{
			name:       "nested path",
			prefix:     "gordon",
			domain:     "app.example.com",
			volumePath: "/var/lib/data",
			expected:   "gordon-app-example-com-var-lib-data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := generateVolumeName(tt.prefix, tt.domain, tt.volumePath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMergeEnvironmentVariables(t *testing.T) {
	dockerEnv := []string{"FOO=docker", "BAR=docker"}
	userEnv := []string{"FOO=user", "BAZ=user"}

	result := mergeEnvironmentVariables(dockerEnv, userEnv)

	// User env should override docker env
	assert.Contains(t, result, "FOO=user")
	assert.Contains(t, result, "BAR=docker")
	assert.Contains(t, result, "BAZ=user")
}
