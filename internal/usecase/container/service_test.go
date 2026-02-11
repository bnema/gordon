package container

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func setupMetricsTest(t *testing.T) (*telemetry.Metrics, *sdkmetric.ManualReader) {
	t.Helper()

	prev := otel.GetMeterProvider()
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
	})

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	otel.SetMeterProvider(mp)
	t.Cleanup(func() {
		_ = mp.Shutdown(context.Background())
	})

	m, err := telemetry.NewMetrics()
	require.NoError(t, err)
	return m, reader
}

func managedMetricState(t *testing.T, reader *sdkmetric.ManualReader) (int64, int, []metricdata.DataPoint[int64]) {
	t.Helper()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gordon.container.managed" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok)
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total, len(sum.DataPoints), sum.DataPoints
		}
	}

	return 0, 0, nil
}

func TestService_ManagedContainersMetric_GlobalSeriesOnDeployReplaceAndRemove(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()
	metrics, reader := setupMetricsTest(t)
	svc.SetMetrics(metrics)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil).Twice()

	// First deploy increments metric.
	svc.activateDeployedContainer(ctx, "test.example.com", &domain.Container{ID: "container-1"})
	value, seriesCount, points := managedMetricState(t, reader)
	assert.Equal(t, int64(1), value)
	assert.Equal(t, 1, seriesCount)
	assert.Equal(t, 0, points[0].Attributes.Len())

	// Replacing an existing domain should not increment.
	svc.activateDeployedContainer(ctx, "test.example.com", &domain.Container{ID: "container-2"})
	value, seriesCount, points = managedMetricState(t, reader)
	assert.Equal(t, int64(1), value)
	assert.Equal(t, 1, seriesCount)
	assert.Equal(t, 0, points[0].Attributes.Len())

	runtime.EXPECT().RemoveContainer(mock.Anything, "container-2", true).Return(nil)
	require.NoError(t, svc.Remove(ctx, "container-2", true))

	value, seriesCount, points = managedMetricState(t, reader)
	assert.Equal(t, int64(0), value)
	assert.Equal(t, 1, seriesCount)
	assert.Equal(t, 0, points[0].Attributes.Len())
}

func TestService_SyncContainers_ManagedContainersMetricTracksDelta(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()
	metrics, reader := setupMetricsTest(t)
	svc.SetMetrics(metrics)

	runtime.EXPECT().ListContainers(mock.Anything, false).Return([]*domain.Container{
		{
			ID: "container-1",
			Labels: map[string]string{
				"gordon.domain":  "app1.example.com",
				"gordon.managed": "true",
			},
		},
		{
			ID: "container-2",
			Labels: map[string]string{
				"gordon.domain":  "app2.example.com",
				"gordon.managed": "true",
			},
		},
	}, nil).Once()
	require.NoError(t, svc.SyncContainers(ctx))

	value, seriesCount, points := managedMetricState(t, reader)
	assert.Equal(t, int64(2), value)
	assert.Equal(t, 1, seriesCount)
	assert.Equal(t, 0, points[0].Attributes.Len())

	// No runtime change: metric should remain stable.
	runtime.EXPECT().ListContainers(mock.Anything, false).Return([]*domain.Container{
		{
			ID: "container-1",
			Labels: map[string]string{
				"gordon.domain":  "app1.example.com",
				"gordon.managed": "true",
			},
		},
		{
			ID: "container-2",
			Labels: map[string]string{
				"gordon.domain":  "app2.example.com",
				"gordon.managed": "true",
			},
		},
	}, nil).Once()
	require.NoError(t, svc.SyncContainers(ctx))

	value, seriesCount, points = managedMetricState(t, reader)
	assert.Equal(t, int64(2), value)
	assert.Equal(t, 1, seriesCount)
	assert.Equal(t, 0, points[0].Attributes.Len())

	// One container removed in runtime: metric should decrement by one.
	runtime.EXPECT().ListContainers(mock.Anything, false).Return([]*domain.Container{
		{
			ID: "container-1",
			Labels: map[string]string{
				"gordon.domain":  "app1.example.com",
				"gordon.managed": "true",
			},
		},
	}, nil).Once()
	require.NoError(t, svc.SyncContainers(ctx))

	value, seriesCount, points = managedMetricState(t, reader)
	assert.Equal(t, int64(1), value)
	assert.Equal(t, 1, seriesCount)
	assert.Equal(t, 0, points[0].Attributes.Len())
}

func TestService_Deploy_Success(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		NetworkIsolation: false,
		VolumeAutoCreate: false,
		ReadinessDelay:   time.Millisecond, // Minimal delay for tests
		DrainDelay:       time.Millisecond,
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

func TestService_Deploy_ReadinessRecoveryWindow_AllowsTransientFlap(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		NetworkIsolation: false,
		VolumeAutoCreate: false,
		ReadinessDelay:   time.Millisecond,
		DrainDelay:       time.Millisecond,
	}

	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:latest").Return(nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return([]string{}, nil)

	createdContainer := &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Image:  "myapp:latest",
		Status: "created",
	}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(createdContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "container-123").Return(nil)

	// Readiness check sequence:
	// 1) Initial "started" check => running
	// 2) Post-delay verification => transient not-running
	// 3) Recovery-window poll => running again
	runtime.EXPECT().IsContainerRunning(mock.Anything, "container-123").Return(true, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "container-123").Return(false, nil).Once()
	runtime.EXPECT().IsContainerRunning(mock.Anything, "container-123").Return(true, nil).Once()

	runtime.EXPECT().InspectContainer(mock.Anything, "container-123").Return(&domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Image:  "myapp:latest",
		Status: "running",
		Ports:  []int{8080},
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "container-123", result.ID)
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

func TestService_PullImage_UsesServiceTokenForExternalPull(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)

	config := Config{
		RegistryAuthEnabled:  true,
		ServiceTokenUsername: "gordon-service",
		ServiceToken:         "service-token",
	}

	svc := NewService(runtime, nil, nil, nil, config)
	ctx := testContext()

	runtime.EXPECT().PullImageWithAuth(mock.Anything, "registry.example.com/myapp:latest", "gordon-service", "service-token").Return(nil)

	err := svc.pullImage(ctx, "registry.example.com/myapp:latest", false)

	assert.NoError(t, err)
}

func TestService_PullImage_RequiresServiceTokenForExternalPull(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)

	config := Config{
		RegistryAuthEnabled: true,
	}

	svc := NewService(runtime, nil, nil, nil, config)
	ctx := testContext()

	err := svc.pullImage(ctx, "registry.example.com/myapp:latest", false)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "registry service token not configured")
}

func TestService_PullImage_InternalRetriesOnConnectionRefused(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)

	config := Config{
		RegistryAuthEnabled:      true,
		InternalRegistryUsername: "internal",
		InternalRegistryPassword: "secret",
	}

	svc := NewService(runtime, nil, nil, nil, config)
	ctx := testContext()

	runtime.EXPECT().PullImageWithAuth(mock.Anything, "localhost:5000/myapp:latest", "internal", "secret").
		Return(errors.New("connection refused")).
		Once()
	runtime.EXPECT().PullImageWithAuth(mock.Anything, "localhost:5000/myapp:latest", "internal", "secret").
		Return(nil).
		Once()

	err := svc.pullImage(ctx, "localhost:5000/myapp:latest", true)

	assert.NoError(t, err)
}

func TestService_Deploy_ReplacesExistingContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)
	cacheInvalidator := mocks.NewMockProxyCacheInvalidator(t)

	config := Config{
		ReadinessDelay: time.Millisecond, // Minimal delay for tests
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	svc.SetProxyCacheInvalidator(cacheInvalidator)
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

	// Synchronous cache invalidation before old container cleanup
	cacheInvalidator.EXPECT().InvalidateTarget(mock.Anything, "test.example.com").Return()

	// Now cleanup old container (after new one is ready + cache invalidated + drain delay)
	runtime.EXPECT().StopContainer(mock.Anything, "old-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "old-container", true).Return(nil)

	// Rename new container to canonical name
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)

	// Ensure the service's tracked container entry now points to the new container.
	tracked, ok := svc.Get(ctx, "test.example.com")
	assert.True(t, ok)
	assert.Equal(t, "new-container", tracked.ID)
}

func TestService_Deploy_SkipRedundantDeploy_GetImageIDError(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	// Existing container has ImageID, but GetImageID will fail
	existingContainer := &domain.Container{
		ID:      "existing-container",
		Name:    "gordon-test.example.com",
		Image:   "myapp:latest",
		ImageID: "sha256:abc123",
		Status:  "running",
	}
	svc.containers["test.example.com"] = existingContainer

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// prepareDeployResources
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:latest"}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return([]string{}, nil)

	// GetImageID fails => deploy proceeds normally (graceful degradation)
	runtime.EXPECT().GetImageID(mock.Anything, "myapp:latest").Return("", errors.New("image inspect failed"))

	// Full deploy proceeds despite the error
	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-new", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID: "new-container", Status: "running",
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	// Old container is finalized
	runtime.EXPECT().StopContainer(mock.Anything, "existing-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "existing-container", true).Return(nil)
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)
}

func TestService_Deploy_WithNetworkIsolation(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		NetworkIsolation: true,
		NetworkPrefix:    "gordon",
		ReadinessDelay:   time.Millisecond, // Minimal delay for tests
		DrainDelay:       time.Millisecond,
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
		ReadinessDelay:   time.Millisecond, // Minimal delay for tests
		DrainDelay:       time.Millisecond,
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

	// Shutdown no longer stops containers — they are left running for
	// SyncContainers + AutoStart to pick back up on next boot.
	err := svc.Shutdown(ctx)

	assert.NoError(t, err)
	// Containers remain tracked (not stopped).
	assert.Len(t, svc.containers, 2)
}

func TestService_Restart_NotFound(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	err := svc.Restart(ctx, "nonexistent.example.com", false)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrContainerNotFound))
}

func TestService_Restart_Success(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["test.example.com"] = &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Status: "running",
	}

	runtime.EXPECT().RestartContainer(mock.Anything, "container-123").Return(nil)

	err := svc.Restart(ctx, "test.example.com", false)

	assert.NoError(t, err)
}

func TestService_Restart_Error(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["test.example.com"] = &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Status: "running",
	}

	runtime.EXPECT().RestartContainer(mock.Anything, "container-123").Return(errors.New("restart failed"))

	err := svc.Restart(ctx, "test.example.com", false)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to restart container")
}

func TestService_Restart_ReconcilesStaleContainerID(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["test.example.com"] = &domain.Container{
		ID:     "stale-container-id",
		Name:   "gordon-test.example.com",
		Status: "running",
	}

	runtime.EXPECT().RestartContainer(mock.Anything, "stale-container-id").
		Return(errors.New("no container with name or ID \"stale-container-id\" found")).
		Once()
	runtime.EXPECT().ListContainers(mock.Anything, false).Return([]*domain.Container{
		{
			ID:   "fresh-container-id",
			Name: "gordon-test.example.com",
			Labels: map[string]string{
				"gordon.domain":  "test.example.com",
				"gordon.managed": "true",
			},
		},
	}, nil).Once()
	runtime.EXPECT().RestartContainer(mock.Anything, "fresh-container-id").Return(nil).Once()

	err := svc.Restart(ctx, "test.example.com", false)

	assert.NoError(t, err)
}

func TestService_Restart_ReconcilesStaleContainerID_NotFoundAfterSync(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["test.example.com"] = &domain.Container{
		ID:     "stale-container-id",
		Name:   "gordon-test.example.com",
		Status: "running",
	}

	runtime.EXPECT().RestartContainer(mock.Anything, "stale-container-id").
		Return(errors.New("no such container")).
		Once()
	runtime.EXPECT().ListContainers(mock.Anything, false).Return([]*domain.Container{}, nil).Once()

	err := svc.Restart(ctx, "test.example.com", false)

	assert.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrContainerNotFound))
}

func TestService_Restart_WithAttachments(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(runtime, envLoader, eventBus, nil, Config{})
	ctx := testContext()

	svc.containers["test.example.com"] = &domain.Container{
		ID:     "container-123",
		Name:   "gordon-test.example.com",
		Status: "running",
	}
	svc.attachments["test.example.com"] = []string{"container-attach-1", "container-attach-2"}

	runtime.EXPECT().RestartContainer(mock.Anything, "container-123").Return(nil)
	runtime.EXPECT().RestartContainer(mock.Anything, "container-attach-1").Return(nil)
	runtime.EXPECT().RestartContainer(mock.Anything, "container-attach-2").Return(fmt.Errorf("boom"))

	err := svc.Restart(ctx, "test.example.com", true)

	assert.NoError(t, err)
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

func TestBuildImageRef(t *testing.T) {
	tests := []struct {
		name                string
		image               string
		registryAuthEnabled bool
		registryDomain      string
		wantRef             string
	}{
		{
			name:                "adds registry domain prefix",
			image:               "myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "registry.example.com",
			wantRef:             "registry.example.com/myapp:latest",
		},
		{
			name:                "keeps existing registry domain prefix",
			image:               "registry.example.com/myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "registry.example.com",
			wantRef:             "registry.example.com/myapp:latest",
		},
		{
			name:                "skips external registry images",
			image:               "docker.io/library/nginx:latest",
			registryAuthEnabled: true,
			registryDomain:      "registry.example.com",
			wantRef:             "docker.io/library/nginx:latest",
		},
		{
			name:                "returns original when auth disabled",
			image:               "myapp:latest",
			registryAuthEnabled: false,
			registryDomain:      "registry.example.com",
			wantRef:             "myapp:latest",
		},
		{
			name:                "localhost domain adds prefix",
			image:               "myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "localhost:5000",
			wantRef:             "localhost:5000/myapp:latest",
		},
		{
			name:                "localhost domain keeps existing prefix",
			image:               "localhost:5000/myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "localhost:5000",
			wantRef:             "localhost:5000/myapp:latest",
		},
		{
			name:                "explicit host:port different than RegistryDomain",
			image:               "localhost:5001/myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "localhost:5000",
			wantRef:             "localhost:5001/myapp:latest",
		},
		{
			name:                "ghcr.io external registry",
			image:               "ghcr.io/owner/repo:latest",
			registryAuthEnabled: true,
			registryDomain:      "localhost:5000",
			wantRef:             "ghcr.io/owner/repo:latest",
		},
		{
			name:                "gcr.io external registry",
			image:               "gcr.io/project/image:v1",
			registryAuthEnabled: true,
			registryDomain:      "registry.example.com",
			wantRef:             "gcr.io/project/image:v1",
		},
		{
			name:                "quay.io external registry",
			image:               "quay.io/org/app:tag",
			registryAuthEnabled: true,
			registryDomain:      "localhost:5000",
			wantRef:             "quay.io/org/app:tag",
		},
		{
			name:                "localhost without port keeps existing prefix",
			image:               "localhost/myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "localhost:5000",
			wantRef:             "localhost/myapp:latest",
		},
		{
			name:                "registry domain with trailing slash",
			image:               "myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "registry.example.com/",
			wantRef:             "registry.example.com/myapp:latest",
		},
		{
			name:                "ipv6 registry without port",
			image:               "[fd00::1]/myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "localhost:5000",
			wantRef:             "[fd00::1]/myapp:latest",
		},
		{
			name:                "ipv6 registry with port",
			image:               "[fd00::1]:5000/myapp:latest",
			registryAuthEnabled: true,
			registryDomain:      "localhost:5000",
			wantRef:             "[fd00::1]:5000/myapp:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{
				config: Config{
					RegistryAuthEnabled: tt.registryAuthEnabled,
					RegistryDomain:      tt.registryDomain,
				},
			}
			gotRef := svc.buildImageRef(tt.image)
			assert.Equal(t, tt.wantRef, gotRef, "unexpected image reference")
		})
	}
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
		ReadinessDelay:           time.Millisecond,
		DrainDelay:               time.Millisecond,
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
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	routes := []domain.Route{
		{Domain: "app1.example.com", Image: "myapp1:latest"},
		{Domain: "app2.example.com", Image: "myapp2:latest"},
	}

	// Setup mocks for route deployments
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil).Times(2)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp1:latest", "myapp2:latest"}, nil).Times(2)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, mock.AnythingOfType("string")).Return([]int{8080}, nil).Times(2)
	envLoader.EXPECT().LoadEnv(mock.Anything, mock.AnythingOfType("string")).Return([]string{}, nil).Times(2)
	runtime.EXPECT().InspectImageEnv(mock.Anything, mock.AnythingOfType("string")).Return([]string{}, nil).Times(2)

	// Create and start containers — readiness is skipped so no IsContainerRunning calls.
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{
		ID: "container-1", Name: "gordon-app1.example.com", Status: "created",
	}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{
		ID: "container-2", Name: "gordon-app2.example.com", Status: "created",
	}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, mock.AnythingOfType("string")).Return(nil).Times(2)
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
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
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

	// Only deploy for app2 (app1 is skipped). Readiness is skipped — no IsContainerRunning.
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil).Once()
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp2:latest"}, nil).Once()
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp2:latest").Return([]int{8080}, nil).Once()
	envLoader.EXPECT().LoadEnv(mock.Anything, "app2.example.com").Return([]string{}, nil).Once()
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp2:latest").Return([]string{}, nil).Once()
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{
		ID: "container-2", Status: "created",
	}, nil).Once()
	runtime.EXPECT().StartContainer(mock.Anything, "container-2").Return(nil).Once()
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

func TestService_AutoStart_UsesInternalDeployContext(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		RegistryDomain: "reg.example.com",
		RegistryPort:   5000,
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)

	// Mark context as internal deploy — this is what syncAndAutoStart should do
	ctx := domain.WithInternalDeploy(testContext())

	routes := []domain.Route{
		{Domain: "app1.example.com", Image: "reg.example.com/myapp:latest"},
	}

	// Key assertion: PullImage should be called with localhost:5000 rewrite,
	// NOT the original reg.example.com/myapp:latest.
	// Readiness is skipped — no IsContainerRunning calls.
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "localhost:5000/myapp:latest").Return(nil)
	runtime.EXPECT().TagImage(mock.Anything, "localhost:5000/myapp:latest", "reg.example.com/myapp:latest").Return(nil)
	runtime.EXPECT().UntagImage(mock.Anything, "localhost:5000/myapp:latest").Return(nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "reg.example.com/myapp:latest").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "app1.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "reg.example.com/myapp:latest").Return([]string{}, nil)
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(&domain.Container{
		ID: "container-1", Name: "gordon-app1.example.com", Status: "created",
	}, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "container-1").Return(nil)
	runtime.EXPECT().InspectContainer(mock.Anything, "container-1").Return(&domain.Container{
		Status: "running",
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	err := svc.AutoStart(ctx, routes)
	assert.NoError(t, err)
}

// TestService_Deploy_OrphanCleanupSkipsTrackedContainer verifies that the orphan cleanup
// does not remove the currently tracked container during zero-downtime deployment.
// This is critical for preventing downtime - the old container must stay running
// until the new container is ready and traffic is switched.
func TestService_Deploy_OrphanCleanupSkipsTrackedContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)
	cacheInvalidator := mocks.NewMockProxyCacheInvalidator(t)

	config := Config{
		ReadinessDelay: time.Millisecond, // Minimal delay for tests
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	svc.SetProxyCacheInvalidator(cacheInvalidator)
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

	// Synchronous cache invalidation
	cacheInvalidator.EXPECT().InvalidateTarget(mock.Anything, "test.example.com").Return()

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
		ReadinessDelay: time.Millisecond, // Minimal delay for tests
		DrainDelay:     time.Millisecond,
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

func TestService_Deploy_OrphanCleanupRemovesStaleNewContainer(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)
	cacheInvalidator := mocks.NewMockProxyCacheInvalidator(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	svc.SetProxyCacheInvalidator(cacheInvalidator)
	ctx := testContext()

	// Tracked canonical container exists, but stale -new container should be removed.
	svc.containers["test.example.com"] = &domain.Container{
		ID:     "tracked-container-123",
		Name:   "gordon-test.example.com",
		Status: "running",
	}

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:v2",
	}

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{
		{
			ID:     "tracked-container-123",
			Name:   "gordon-test.example.com",
			Status: "running",
		},
		{
			ID:     "stale-new-container",
			Name:   "gordon-test.example.com-new",
			Status: "exited",
		},
	}, nil)
	runtime.EXPECT().StopContainer(mock.Anything, "stale-new-container").Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, "stale-new-container", true).Return(nil).Once()

	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:v2").Return(nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:v2").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:v2").Return([]string{}, nil)

	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-new", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.Name == "gordon-test.example.com-new"
	})).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID:     "new-container",
		Status: "running",
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	// Synchronous cache invalidation
	cacheInvalidator.EXPECT().InvalidateTarget(mock.Anything, "test.example.com").Return()

	runtime.EXPECT().StopContainer(mock.Anything, "tracked-container-123").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "tracked-container-123", true).Return(nil)
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "new-container", result.ID)
}

func TestService_Deploy_TrackedTempContainerUsesAlternateTempName(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)
	cacheInvalidator := mocks.NewMockProxyCacheInvalidator(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	svc.SetProxyCacheInvalidator(cacheInvalidator)
	ctx := testContext()

	// Simulate an interrupted zero-downtime deploy that left "-new" as the tracked container name.
	svc.containers["test.example.com"] = &domain.Container{
		ID:     "tracked-temp-container",
		Name:   "gordon-test.example.com-new",
		Status: "running",
	}

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:v2",
	}

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{
		{
			ID:     "tracked-temp-container",
			Name:   "gordon-test.example.com-new",
			Status: "running",
		},
	}, nil)

	runtime.EXPECT().ListImages(mock.Anything).Return([]string{}, nil)
	runtime.EXPECT().PullImage(mock.Anything, "myapp:v2").Return(nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:v2").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:v2").Return([]string{}, nil)

	created := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-next", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.MatchedBy(func(cfg *domain.ContainerConfig) bool {
		return cfg.Name == "gordon-test.example.com-next"
	})).Return(created, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID:     "new-container",
		Status: "running",
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	// Synchronous cache invalidation
	cacheInvalidator.EXPECT().InvalidateTarget(mock.Anything, "test.example.com").Return()

	runtime.EXPECT().StopContainer(mock.Anything, "tracked-temp-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "tracked-temp-container", true).Return(nil)
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "new-container", result.ID)
}

func TestService_StripRegistryPrefix(t *testing.T) {
	tests := []struct {
		name           string
		registryDomain string
		image          string
		expected       string
	}{
		{
			name:           "strips registry prefix",
			registryDomain: "reg.example.com",
			image:          "reg.example.com/myapp:latest",
			expected:       "myapp:latest",
		},
		{
			name:           "strips registry prefix with trailing slash in domain",
			registryDomain: "reg.example.com/",
			image:          "reg.example.com/myapp:v1.0",
			expected:       "myapp:v1.0",
		},
		{
			name:           "preserves image without registry prefix",
			registryDomain: "reg.example.com",
			image:          "nginx:latest",
			expected:       "nginx:latest",
		},
		{
			name:           "preserves image with different registry",
			registryDomain: "reg.example.com",
			image:          "gcr.io/project/image:latest",
			expected:       "gcr.io/project/image:latest",
		},
		{
			name:           "handles empty registry domain",
			registryDomain: "",
			image:          "reg.example.com/myapp:latest",
			expected:       "reg.example.com/myapp:latest",
		},
		{
			name:           "handles nested paths",
			registryDomain: "reg.example.com",
			image:          "reg.example.com/org/repo/app:latest",
			expected:       "org/repo/app:latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := mocks.NewMockContainerRuntime(t)
			envLoader := mocks.NewMockEnvLoader(t)
			eventBus := mocks.NewMockEventPublisher(t)

			config := Config{
				RegistryDomain: tt.registryDomain,
			}
			svc := NewService(runtime, envLoader, eventBus, nil, config)

			result := svc.stripRegistryPrefix(tt.image)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		domain      string
		expected    string
		description string
	}{
		{
			domain:      "git.example.com",
			expected:    "git__example__com",
			description: "Dots become double underscores",
		},
		{
			domain:      "git-example.com",
			expected:    "git-example__com",
			description: "Hyphens preserved, dots become underscores",
		},
		{
			domain:      "app:8080.example.com",
			expected:    "app-_8080__example__com",
			description: "Colons become hyphen-underscore",
		},
		{
			domain:      "git.example.com:3000",
			expected:    "git__example__com-_3000",
			description: "Multiple separators handled distinctly",
		},
		{
			domain:      "simple.com",
			expected:    "simple__com",
			description: "Simple domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			result := sanitizeName(tt.domain)
			assert.Equal(t, tt.expected, result, "sanitization should match expected")
		})
	}

	// Verify no collisions between potentially conflicting domains
	t.Run("NoCollisions", func(t *testing.T) {
		domains := []string{
			"git.example.com",
			"git-example.com",
			"app:8080.example.com",
			"app-8080-example.com",
		}

		results := make(map[string]string)
		for _, d := range domains {
			result := sanitizeName(d)
			if original, exists := results[result]; exists {
				t.Errorf("COLLISION: %q and %q both sanitize to %q", original, d, result)
			}
			results[result] = d
		}
	})
}

func TestService_Deploy_ConcurrentSameDomain(t *testing.T) {
	// Verify that concurrent Deploy calls for the same domain are serialized
	// and both succeed without container name conflicts.
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)
	cacheInvalidator := mocks.NewMockProxyCacheInvalidator(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	svc.SetProxyCacheInvalidator(cacheInvalidator)
	ctx := testContext()

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// Track call order to verify serialization
	var callOrder []string
	var callMu sync.Mutex

	// Setup mocks that will be called by both deploys sequentially.
	// First deploy: no existing container → creates gordon-test.example.com
	// Second deploy: sees first container → creates gordon-test.example.com-new

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil).Times(2)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:latest"}, nil).Times(2)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil).Times(2)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return(nil, nil).Times(2)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return(nil, nil).Times(2)

	createCall := 0
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).
		RunAndReturn(func(_ context.Context, cfg *domain.ContainerConfig) (*domain.Container, error) {
			callMu.Lock()
			createCall++
			n := createCall
			callOrder = append(callOrder, fmt.Sprintf("create-%d:%s", n, cfg.Name))
			callMu.Unlock()
			return &domain.Container{
				ID:     fmt.Sprintf("container-%d", n),
				Name:   cfg.Name,
				Image:  cfg.Image,
				Status: "created",
			}, nil
		})

	runtime.EXPECT().StartContainer(mock.Anything, mock.AnythingOfType("string")).Return(nil).Times(2)
	// IsContainerRunning is called 2x per deploy in waitForReady (initial check + after delay)
	runtime.EXPECT().IsContainerRunning(mock.Anything, mock.AnythingOfType("string")).Return(true, nil).Times(4)

	runtime.EXPECT().InspectContainer(mock.Anything, mock.AnythingOfType("string")).
		RunAndReturn(func(_ context.Context, id string) (*domain.Container, error) {
			// Return correct container name based on ID
			// container-1 is the first deploy (canonical name)
			// container-2 is the second deploy (with -new suffix)
			name := "gordon-test.example.com"
			if id == "container-2" {
				name = "gordon-test.example.com-new"
			}
			return &domain.Container{
				ID:     id,
				Name:   name,
				Image:  "myapp:latest",
				Status: "running",
				Ports:  []int{8080},
			}, nil
		})

	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.Anything).Return(nil).Times(2)

	// Both deploys call InvalidateTarget (to ensure proxy picks up the new container).
	cacheInvalidator.EXPECT().InvalidateTarget(mock.Anything, "test.example.com").Return().Times(2)

	// which means it will also stop+remove+rename the old container.
	runtime.EXPECT().StopContainer(mock.Anything, mock.AnythingOfType("string")).Return(nil).Once()
	runtime.EXPECT().RemoveContainer(mock.Anything, mock.AnythingOfType("string"), true).Return(nil).Once()
	runtime.EXPECT().RenameContainer(mock.Anything, mock.AnythingOfType("string"), mock.AnythingOfType("string")).Return(nil).Once()

	// Launch two concurrent deploys
	var wg sync.WaitGroup
	errs := make([]error, 2)

	wg.Add(2)
	for i := range 2 {
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = svc.Deploy(ctx, route)
		}(i)
	}
	wg.Wait()

	// Both should succeed (second deploy waits for the first to finish)
	assert.NoError(t, errs[0], "first deploy should succeed")
	assert.NoError(t, errs[1], "second deploy should succeed")

	// Verify creates were serialized (not interleaved)
	callMu.Lock()
	assert.Len(t, callOrder, 2, "should have exactly 2 create calls")
	// First deploy uses canonical name, second uses -new suffix (zero-downtime)
	assert.Equal(t, "create-1:gordon-test.example.com", callOrder[0], "first create should use canonical name")
	assert.Contains(t, callOrder[1], "gordon-test.example.com-new", "second create should use -new suffix")
	callMu.Unlock()
}

func TestService_Deploy_ContextCancellation(t *testing.T) {
	// Verify that deploy lock acquisition respects context cancellation.
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// Test context cancelled before lock acquisition.
	cancelledCtx, cancel := context.WithCancel(testContext())
	cancel() // Cancel immediately

	_, err := svc.Deploy(cancelledCtx, route)
	assert.Error(t, err, "deploy should fail with cancelled context")
	assert.ErrorIs(t, err, context.Canceled, "error should be context.Canceled")
}

// TestService_Deploy_CacheInvalidationBeforeOldContainerStop verifies the fix for
// the proxy cache race condition: InvalidateTarget must be called synchronously
// BEFORE the old container is stopped, preventing 503 errors during deployment.
func TestService_Deploy_CacheInvalidationBeforeOldContainerStop(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)
	cacheInvalidator := mocks.NewMockProxyCacheInvalidator(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	svc.SetProxyCacheInvalidator(cacheInvalidator)
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

	// Track ordering of cache invalidation and container stop
	var callOrder []string
	var orderMu sync.Mutex

	// Standard deploy mocks
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:v2"}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:v2").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:v2").Return([]string{}, nil)

	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-new", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID: "new-container", Status: "running",
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	// Track: cache invalidation should happen first
	cacheInvalidator.EXPECT().InvalidateTarget(mock.Anything, "test.example.com").
		Run(func(_ context.Context, _ string) {
			orderMu.Lock()
			callOrder = append(callOrder, "invalidate_cache")
			orderMu.Unlock()
		}).Return()

	// Track: stop should happen after invalidation
	runtime.EXPECT().StopContainer(mock.Anything, "old-container").
		RunAndReturn(func(_ context.Context, _ string) error {
			orderMu.Lock()
			callOrder = append(callOrder, "stop_old_container")
			orderMu.Unlock()
			return nil
		})
	runtime.EXPECT().RemoveContainer(mock.Anything, "old-container", true).Return(nil)
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)

	// Verify ordering: cache invalidation MUST happen before old container stop
	orderMu.Lock()
	defer orderMu.Unlock()
	assert.Equal(t, []string{"invalidate_cache", "stop_old_container"}, callOrder,
		"InvalidateTarget must be called before StopContainer to prevent 503 errors")
}

// TestService_Deploy_NilCacheInvalidator verifies that deploy works gracefully
// when no cache invalidator is set (e.g., in tests or minimal configurations).
func TestService_Deploy_NilCacheInvalidator(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	// Intentionally NOT setting cache invalidator
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	// Pre-populate with existing container
	svc.containers["test.example.com"] = &domain.Container{
		ID:     "old-container",
		Name:   "gordon-test.example.com",
		Status: "running",
	}

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:v2",
	}

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:v2"}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:v2").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:v2").Return([]string{}, nil)

	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-new", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID: "new-container", Status: "running",
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)
	runtime.EXPECT().StopContainer(mock.Anything, "old-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "old-container", true).Return(nil)
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)
}

func TestService_Deploy_SkipsRedundantDeploy(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	// Pre-populate with existing container that has an ImageID (set by InspectContainer).
	// This simulates the first deploy (from image.pushed event) having already completed.
	existingContainer := &domain.Container{
		ID:      "existing-container",
		Name:    "gordon-test.example.com",
		Image:   "myapp:latest",
		ImageID: "sha256:abc123",
		Status:  "running",
	}
	svc.containers["test.example.com"] = existingContainer

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// prepareDeployResources will run: orphan cleanup, image pull, etc.
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:latest"}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return([]string{}, nil)

	// skipRedundantDeploy: resolve image ID and compare with existing container
	runtime.EXPECT().GetImageID(mock.Anything, "myapp:latest").Return("sha256:abc123", nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "existing-container").Return(true, nil)

	// No CreateContainer, StartContainer, or readiness calls should happen

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "existing-container", result.ID)

	// Container should still be tracked
	tracked, exists := svc.Get(ctx, "test.example.com")
	assert.True(t, exists)
	assert.Equal(t, "existing-container", tracked.ID)
}

func TestService_Deploy_DoesNotSkipWhenImageIDDiffers(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)
	cacheInvalidator := mocks.NewMockProxyCacheInvalidator(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	svc.SetProxyCacheInvalidator(cacheInvalidator)
	ctx := testContext()

	// Existing container with a DIFFERENT image ID than what's being deployed
	existingContainer := &domain.Container{
		ID:      "old-container",
		Name:    "gordon-test.example.com",
		Image:   "myapp:latest",
		ImageID: "sha256:old-image",
		Status:  "running",
	}
	svc.containers["test.example.com"] = existingContainer

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// prepareDeployResources
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:latest"}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return([]string{}, nil)

	// skipRedundantDeploy: image IDs differ, so deploy proceeds
	runtime.EXPECT().GetImageID(mock.Anything, "myapp:latest").Return("sha256:new-image", nil)

	// Full deploy proceeds
	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-new", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID: "new-container", Status: "running",
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)
	cacheInvalidator.EXPECT().InvalidateTarget(mock.Anything, "test.example.com").Return()
	runtime.EXPECT().StopContainer(mock.Anything, "old-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "old-container", true).Return(nil)
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)
}

func TestService_Deploy_SkipRedundantDeploy_ContainerNotRunning(t *testing.T) {
	runtime := mocks.NewMockContainerRuntime(t)
	envLoader := mocks.NewMockEnvLoader(t)
	eventBus := mocks.NewMockEventPublisher(t)

	config := Config{
		ReadinessDelay: time.Millisecond,
		DrainDelay:     time.Millisecond,
	}
	svc := NewService(runtime, envLoader, eventBus, nil, config)
	ctx := testContext()

	// Existing container has same image ID but is NOT running (crashed)
	existingContainer := &domain.Container{
		ID:      "existing-container",
		Name:    "gordon-test.example.com",
		Image:   "myapp:latest",
		ImageID: "sha256:abc123",
		Status:  "exited",
	}
	svc.containers["test.example.com"] = existingContainer

	route := domain.Route{
		Domain: "test.example.com",
		Image:  "myapp:latest",
	}

	// prepareDeployResources
	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{}, nil)
	runtime.EXPECT().ListImages(mock.Anything).Return([]string{"myapp:latest"}, nil)
	runtime.EXPECT().GetImageExposedPorts(mock.Anything, "myapp:latest").Return([]int{8080}, nil)
	envLoader.EXPECT().LoadEnv(mock.Anything, "test.example.com").Return([]string{}, nil)
	runtime.EXPECT().InspectImageEnv(mock.Anything, "myapp:latest").Return([]string{}, nil)

	// skipRedundantDeploy: same image ID but container not running => proceed with deploy
	runtime.EXPECT().GetImageID(mock.Anything, "myapp:latest").Return("sha256:abc123", nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "existing-container").Return(false, nil)

	// Full deploy proceeds (replaces crashed container)
	newContainer := &domain.Container{ID: "new-container", Name: "gordon-test.example.com-new", Status: "created"}
	runtime.EXPECT().CreateContainer(mock.Anything, mock.AnythingOfType("*domain.ContainerConfig")).Return(newContainer, nil)
	runtime.EXPECT().StartContainer(mock.Anything, "new-container").Return(nil)
	runtime.EXPECT().IsContainerRunning(mock.Anything, "new-container").Return(true, nil).Times(2)
	runtime.EXPECT().InspectContainer(mock.Anything, "new-container").Return(&domain.Container{
		ID: "new-container", Status: "running",
	}, nil)
	eventBus.EXPECT().Publish(domain.EventContainerDeployed, mock.AnythingOfType("*domain.ContainerEventPayload")).Return(nil)

	// Old (exited) container is still finalized
	runtime.EXPECT().StopContainer(mock.Anything, "existing-container").Return(nil)
	runtime.EXPECT().RemoveContainer(mock.Anything, "existing-container", true).Return(nil)
	runtime.EXPECT().RenameContainer(mock.Anything, "new-container", "gordon-test.example.com").Return(nil)

	result, err := svc.Deploy(ctx, route)

	assert.NoError(t, err)
	assert.Equal(t, "new-container", result.ID)
}
