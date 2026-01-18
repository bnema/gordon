package health

import (
	"context"
	"errors"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestService_CheckRoute_ContainerNotFound(t *testing.T) {
	configSvc := mocks.NewMockConfigService(t)
	containerSvc := mocks.NewMockContainerService(t)
	prober := mocks.NewMockHTTPProber(t)

	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(nil, false)

	svc := NewService(configSvc, containerSvc, prober, testLogger())
	route := domain.Route{Domain: "app.example.com", Image: "myapp:latest"}

	health := svc.CheckRoute(context.Background(), route)

	assert.Equal(t, "app.example.com", health.Domain)
	assert.Equal(t, "not found", health.ContainerStatus)
	assert.Equal(t, 0, health.HTTPStatus)
	assert.False(t, health.Healthy)
	assert.Equal(t, "container not found", health.Error)
}

func TestService_CheckRoute_ContainerNotRunning(t *testing.T) {
	configSvc := mocks.NewMockConfigService(t)
	containerSvc := mocks.NewMockContainerService(t)
	prober := mocks.NewMockHTTPProber(t)

	container := &domain.Container{
		ID:     "abc123",
		Status: "stopped",
	}
	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(container, true)

	svc := NewService(configSvc, containerSvc, prober, testLogger())
	route := domain.Route{Domain: "app.example.com", Image: "myapp:latest"}

	health := svc.CheckRoute(context.Background(), route)

	assert.Equal(t, "app.example.com", health.Domain)
	assert.Equal(t, "stopped", health.ContainerStatus)
	assert.Equal(t, 0, health.HTTPStatus)
	assert.False(t, health.Healthy)
	assert.Contains(t, health.Error, "container is stopped")
}

func TestService_CheckRoute_HTTPProbeSuccess(t *testing.T) {
	configSvc := mocks.NewMockConfigService(t)
	containerSvc := mocks.NewMockContainerService(t)
	prober := mocks.NewMockHTTPProber(t)

	container := &domain.Container{
		ID:     "abc123",
		Status: "running",
	}
	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(container, true)
	prober.EXPECT().Probe(mock.Anything, "https://app.example.com/").Return(200, int64(45), nil)

	svc := NewService(configSvc, containerSvc, prober, testLogger())
	route := domain.Route{Domain: "app.example.com", Image: "myapp:latest"}

	health := svc.CheckRoute(context.Background(), route)

	assert.Equal(t, "app.example.com", health.Domain)
	assert.Equal(t, "running", health.ContainerStatus)
	assert.Equal(t, 200, health.HTTPStatus)
	assert.Equal(t, int64(45), health.ResponseTimeMs)
	assert.True(t, health.Healthy)
	assert.Empty(t, health.Error)
}

func TestService_CheckRoute_HTTPProbeFailure(t *testing.T) {
	configSvc := mocks.NewMockConfigService(t)
	containerSvc := mocks.NewMockContainerService(t)
	prober := mocks.NewMockHTTPProber(t)

	container := &domain.Container{
		ID:     "abc123",
		Status: "running",
	}
	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(container, true)
	prober.EXPECT().Probe(mock.Anything, "https://app.example.com/").Return(0, int64(0), errors.New("connection refused"))

	svc := NewService(configSvc, containerSvc, prober, testLogger())
	route := domain.Route{Domain: "app.example.com", Image: "myapp:latest"}

	health := svc.CheckRoute(context.Background(), route)

	assert.Equal(t, "app.example.com", health.Domain)
	assert.Equal(t, "running", health.ContainerStatus)
	assert.Equal(t, 0, health.HTTPStatus)
	assert.False(t, health.Healthy)
	assert.Equal(t, "connection refused", health.Error)
}

func TestService_CheckRoute_HTTPStatus5xx(t *testing.T) {
	configSvc := mocks.NewMockConfigService(t)
	containerSvc := mocks.NewMockContainerService(t)
	prober := mocks.NewMockHTTPProber(t)

	container := &domain.Container{
		ID:     "abc123",
		Status: "running",
	}
	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(container, true)
	prober.EXPECT().Probe(mock.Anything, "https://app.example.com/").Return(502, int64(100), nil)

	svc := NewService(configSvc, containerSvc, prober, testLogger())
	route := domain.Route{Domain: "app.example.com", Image: "myapp:latest"}

	health := svc.CheckRoute(context.Background(), route)

	assert.Equal(t, "app.example.com", health.Domain)
	assert.Equal(t, "running", health.ContainerStatus)
	assert.Equal(t, 502, health.HTTPStatus)
	assert.Equal(t, int64(100), health.ResponseTimeMs)
	assert.False(t, health.Healthy) // 5xx is not healthy
	assert.Empty(t, health.Error)
}

func TestService_CheckAllRoutes(t *testing.T) {
	configSvc := mocks.NewMockConfigService(t)
	containerSvc := mocks.NewMockContainerService(t)
	prober := mocks.NewMockHTTPProber(t)

	routes := []domain.Route{
		{Domain: "app1.example.com", Image: "app1:latest"},
		{Domain: "app2.example.com", Image: "app2:latest"},
	}
	configSvc.EXPECT().GetRoutes(mock.Anything).Return(routes)

	container1 := &domain.Container{ID: "abc", Status: "running"}
	container2 := &domain.Container{ID: "def", Status: "stopped"}
	containerSvc.EXPECT().Get(mock.Anything, "app1.example.com").Return(container1, true)
	containerSvc.EXPECT().Get(mock.Anything, "app2.example.com").Return(container2, true)

	// Only running containers get probed
	prober.EXPECT().Probe(mock.Anything, "https://app1.example.com/").Return(200, int64(30), nil)

	svc := NewService(configSvc, containerSvc, prober, testLogger())

	results := svc.CheckAllRoutes(context.Background())

	assert.Len(t, results, 2)

	assert.NotNil(t, results["app1.example.com"])
	assert.Equal(t, "running", results["app1.example.com"].ContainerStatus)
	assert.Equal(t, 200, results["app1.example.com"].HTTPStatus)
	assert.True(t, results["app1.example.com"].Healthy)

	assert.NotNil(t, results["app2.example.com"])
	assert.Equal(t, "stopped", results["app2.example.com"].ContainerStatus)
	assert.Equal(t, 0, results["app2.example.com"].HTTPStatus)
	assert.False(t, results["app2.example.com"].Healthy)
}

func TestService_CheckAllRoutes_NoRoutes(t *testing.T) {
	configSvc := mocks.NewMockConfigService(t)
	containerSvc := mocks.NewMockContainerService(t)
	prober := mocks.NewMockHTTPProber(t)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{})

	svc := NewService(configSvc, containerSvc, prober, testLogger())

	results := svc.CheckAllRoutes(context.Background())

	assert.Empty(t, results)
}
