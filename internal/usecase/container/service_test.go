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
		ReadinessDelay:   0, // No delay for tests
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

	// Wait for ready: IsContainerRunning (first check returns true) + verify after delay
	runtime.EXPECT().IsContainerRunning(mock.Anything, "container-123").Return(true, nil).Times(2)

	// Re-inspect after start
	runningContainer := &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Image:  "myapp:latest",
		Status: "running",
		Ports:  []int{8080},
	}
	runtime.EXPECT().InspectContainer(mock.Anything, "container-123").Return(runningContainer, nil)

	// Publish container deployed event
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

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

	config := Config{
		ReadinessDelay: 0, // No delay for tests
	}
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

	// Cleanup orphans
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)

	// Image operations
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:v2").Return(nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:v2").Return([]int{8080}, nil)

	// Environment
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:v2").Return([]string{}, nil)

	// Create new container (with -new suffix for zero-downtime)
	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-new", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.Name == "gordon-test.example.com-new"
	})).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)

	// Wait for ready
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)

	// Inspect after ready
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID:     "new-container",
		Status: "running",
	}, nil)

	// Publish event
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	// Now cleanup old container (after new one is ready)
	runtime.EXPECT().StopContainer(mock.Anything, "old-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "old-container", true).Return(nil)

	// Rename new container to canonical name
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

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
		ReadinessDelay:   0, // No delay for tests
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

	// Wait for ready
	runtime.EXPECT().IsContainerRunning(mock.Anything, "container-123").Return(true, nil).Times(2)

	runtime.EXPECT().InspectContainer(mock.Anything, "container-123").Return(&domain.Container{
		ID:     "container-123",
		Status: "running",
	}, nil)

	// Publish event
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

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
		ReadinessDelay:   0, // No delay for tests
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

	// Wait for ready
	runtime.EXPECT().IsContainerRunning(mock.Anything, "container-123").Return(true, nil).Times(2)

	runtime.EXPECT().InspectContainer(mock.Anything, "container-123").Return(&domain.Container{
		ID:     "container-123",
		Status: "running",
	}, nil)

	// Publish event
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

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
		{
			name:     "localhost with port",
			input:    "localhost:5000/myapp:latest",
			expected: "localhost:5000/myapp",
		},
		{
			name:     "localhost with port no tag",
			input:    "localhost:5000/myapp",
			expected: "localhost:5000/myapp",
		},
		{
			name:     "registry domain with port",
			input:    "registry.example.com:5000/image:v1.0",
			expected: "registry.example.com:5000/image",
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

func TestRewriteToRegistryDomain(t *testing.T) {
	tests := []struct {
		name           string
		imageRef       string
		registryDomain string
		wantRef        string
	}{
		{
			name:           "keeps registry domain prefix",
			imageRef:       "registry.example.com/myapp:latest",
			registryDomain: "registry.example.com",
			wantRef:        "registry.example.com/myapp:latest",
		},
		{
			name:           "prefixes registry domain",
			imageRef:       "myapp:v1.0",
			registryDomain: "registry.example.com",
			wantRef:        "registry.example.com/myapp:v1.0",
		},
		{
			name:           "prefixes external image",
			imageRef:       "docker.io/library/nginx:latest",
			registryDomain: "registry.example.com",
			wantRef:        "registry.example.com/docker.io/library/nginx:latest",
		},
		{
			name:           "empty registry domain returns original",
			imageRef:       "registry.example.com/myapp:latest",
			registryDomain: "",
			wantRef:        "registry.example.com/myapp:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRef := rewriteToRegistryDomain(tt.imageRef, tt.registryDomain)
			assert.Equal(t, tt.wantRef, gotRef, "unexpected rewritten reference")
		})
	}
}

func TestRewriteToLocalRegistry(t *testing.T) {
	tests := []struct {
		name           string
		imageRef       string
		registryDomain string
		registryPort   int
		wantRef        string
	}{
		{
			name:           "rewrites registry domain prefix",
			imageRef:       "registry.example.com/myapp:latest",
			registryDomain: "registry.example.com",
			registryPort:   5000,
			wantRef:        "localhost:5000/myapp:latest",
		},
		{
			name:           "prefixes local registry when no domain",
			imageRef:       "myapp:v1.0",
			registryDomain: "registry.example.com",
			registryPort:   5000,
			wantRef:        "localhost:5000/myapp:v1.0",
		},
		{
			name:           "keeps existing localhost prefix",
			imageRef:       "localhost:5000/myapp:latest",
			registryDomain: "registry.example.com",
			registryPort:   5000,
			wantRef:        "localhost:5000/myapp:latest",
		},
		{
			name:           "prefixes external image path",
			imageRef:       "docker.io/library/nginx:latest",
			registryDomain: "registry.example.com",
			registryPort:   5000,
			wantRef:        "localhost:5000/docker.io/library/nginx:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRef := rewriteToLocalRegistry(tt.imageRef, tt.registryDomain, tt.registryPort)
			assert.Equal(t, tt.wantRef, gotRef, "unexpected rewritten reference")
		})
	}
}

func TestService_Deploy_InternalDeployForcesPull(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		RegistryAuthEnabled:      true,
		RegistryDomain:           "registry.example.com",
		RegistryPort:             5000,
		InternalRegistryUsername: "internal",
		InternalRegistryPassword: "secret",
		ReadinessDelay:           0,
	}

	svc := NewService(runtime, envLoader, eventBus, nil, config)

	// Use internal deploy context (simulating image push event)
	ctx := domain.WithInternalDeploy(testContext())

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// Setup mocks - no orphaned containers
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)

	// Even though image exists locally, internal deploy should force pull
	// Note: ensureLocalImage returns false for internal deploy, skipping the ListImages check
	runtime.EXPECT().PullImageWithAuth(mock.Anything, "localhost:5000/myapp:latest", "internal", "secret").Return(nil)

	// Tag image to canonical name
	runtime.EXPECT().TagImage(mock.Anything, "localhost:5000/myapp:latest", "registry.example.com/myapp:latest").Return(nil)
	runtime.EXPECT().UntagImage(mock.Anything, "localhost:5000/myapp:latest").Return(nil)

	// Get exposed ports
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "registry.example.com/myapp:latest").Return([]int{8080}, nil)

	// Load environment
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "registry.example.com/myapp:latest").Return([]string{}, nil)

	// Create and start container
	createdContainer := &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Status: "created",
	}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(createdContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "container-123").Return(nil)

	// Wait for ready
	runtime.EXPECT().IsContainerRunning(mock.Anything, "container-123").Return(true, nil).Times(2)

	// Re-inspect after start
	runningContainer := &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Status: "running",
		Ports:  []int{8080},
	}
	runtime.EXPECT().InspectContainer(mock.Anything, "container-123").Return(runningContainer, nil)

	// Publish event
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "container-123", result.ID)
}

func TestService_AutoStart_StartsNewContainers(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: 0,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	routes := []domain.Route{
		{Domain: "app1.example.com", Image: "myapp1:latest"},
		{Domain: "app2.example.com", Image: "myapp2:latest"},
	}

	// Setup mocks for first route deployment
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil).Times(2)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp1:latest", "myapp2:latest"}, nil).Times(2)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, mock.AnythingOfType("string")).Return([]int{8080}, nil).Times(2)
	envLoader.EXPECT().LoadEnv(mock.Anything, mock.AnythingOfType("string")).Return([]string{}, nil).Times(2)
	runtime.EXPECT().InspectImageEnv(mock.Anything, mock.AnythingOfType("string")).Return([]string{}, nil).Times(2)

	// Create and start containers
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{
		ID: "container-1", Name: "gordon-app1.example.com", Status: "created",
	}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{
		ID: "container-2", Name: "gordon-app2.example.com", Status: "created",
	}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, mock.AnythingOfType("string")).Return(nil).Times(2)
	runtime.EXPECT().IsContainerRunning(mock.Anything, mock.AnythingOfType("string")).Return(true, nil).Times(4)
	runtime.EXPECT().InspectContainer(mock.Anything, mock.AnythingOfType("string")).Return(&domain.Container{
		Status: "running",
	}, nil).Times(2)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil).Times(2)

	err := svc.AutoStart(ctx, routes)

	assert.NoError(t, err)
	assert.Len(t, svc.containers, 2)
}

func TestService_AutoStart_SkipsExistingContainers(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: 0,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	// Pre-populate with existing container
	svc.containers["app1.example.com"] = &domain.Container{
		ID: "existing-container",
	}

	routes := []domain.Route{
		{Domain: "app1.example.com", Image: "myapp1:latest"}, // Already exists
		{Domain: "app2.example.com", Image: "myapp2:latest"}, // New route
	}

	// Only deploy for app2 (app1 is skipped)
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil).Once()
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp2:latest"}, nil).Once()
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp2:latest").Return([]int{8080}, nil).Once()
	envLoader.EXPECT().LoadEnv(mock.Anything, "app2.example.com").Return([]string{}, nil).Once()
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp2:latest").Return([]string{}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{
		ID: "container-2", Status: "created",
	}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "container-2").Return(nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "container-2").Return(true, nil).Times(2)
	runtime.EXPECT().InspectContainer(mock.Anything, "container-2").Return(&domain.Container{
		Status: "running",
	}, nil).Once()
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil).Once()

	err := svc.AutoStart(ctx, routes)

	assert.NoError(t, err)
	assert.Len(t, svc.containers, 2)
}

func TestService_AutoStart_HandlesDeployErrors(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	routes := []domain.Route{
		{Domain: "app1.example.com", Image: "myapp1:latest"},
	}

	// Setup mocks for failure
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp1:latest").Return(errors.New("image not found"))

	err := svc.AutoStart(ctx, routes)

	// AutoStart should return error when some deployments fail
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "auto-start completed with 1 errors")
}

// TestService_Deploy_OrphanCleanupSkipsTrackedContainer verifies that the orphan cleanup
// does not remove the currently tracked container during zero-downtime deployment.
// This is critical for preventing downtime - the old container must stay running
// until the new container is ready and traffic is switched.
func TestService_Deploy_OrphanCleanupSkipsTrackedContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: 0, // No delay for tests
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	// Pre-populate with existing tracked container
	existingContainer := &domain.Container{
		ID:     "tracked-container-123",
		Name:   "gordon-test.example.com",
		Status: "running",
	}
	svc.containers["test.example.com"] = existingContainer

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:v2",
	}

	// ListContainers returns the tracked container - this simulates the orphan check
	// finding a container with the same name. The bug was that it would remove this
	// container BEFORE the new one was ready, causing downtime.
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{
		{
			ID:     "tracked-container-123",
			Name:   "gordon-test.example.com",
			Status: "running",
		},
	}, nil)

	// Image operations
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:v2").Return(nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:v2").Return([]int{8080}, nil)

	// Environment
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:v2").Return([]string{}, nil)

	// Create new container with -new suffix for zero-downtime
	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-new", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.Name == "gordon-test.example.com-new"
	})).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)

	// Wait for ready
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)

	// Inspect after ready
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID:     "new-container",
		Status: "running",
	}, nil)

	// Publish event
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	// NOW (after new container is ready) the old container should be stopped and removed
	// This is the correct zero-downtime sequence - not during orphan cleanup
	runtime.EXPECT().StopContainer(mock.Anything, "tracked-container-123").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "tracked-container-123", true).Return(nil)

	// Rename new container to canonical name
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)

	// Verify new container is now tracked
	tracked, exists := svc.Get(ctx, "test.example.com")
	assert.True(t, exists)
	assert.Equal(t, "new-container", tracked.ID)
}

// TestService_Deploy_OrphanCleanupRemovesTrueOrphans verifies that containers
// with the same name but NOT tracked are properly removed as orphans.
func TestService_Deploy_OrphanCleanupRemovesTrueOrphans(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: 0, // No delay for tests
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	// NO tracked container - service is empty for this domain

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:v1",
	}

	// ListContainers returns an orphaned container (same name, but not tracked)
	// This could happen if Gordon crashed and restarted, or container was created manually
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{
		{
			ID:     "orphan-container",
			Name:   "gordon-test.example.com",
			Status: "running",
		},
	}, nil)

	// Orphan should be stopped and removed BEFORE we proceed
	runtime.EXPECT().StopContainer(mock.Anything, "orphan-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "orphan-container", true).Return(nil)

	// Image operations
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:v1").Return(nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:v1").Return([]int{8080}, nil)

	// Environment
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:v1").Return([]string{}, nil)

	// Create container (no -new suffix since no existing tracked container)
	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.Name == "gordon-test.example.com"
	})).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)

	// Wait for ready
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)

	// Inspect after ready
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID:     "new-container",
		Status: "running",
		Ports:  []int{8080},
	}, nil)

	// Publish event
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)

	// Verify new container is tracked
	tracked, exists := svc.Get(ctx, "test.example.com")
	assert.True(t, exists)
	assert.Equal(t, "new-container", tracked.ID)
}
