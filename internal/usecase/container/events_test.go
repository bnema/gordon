package container

import (
	"context"
	"errors"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testCtx() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

// ImagePushedHandler tests

func TestImagePushedHandler_CanHandle(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	assert.True(t, handler.CanHandle(domain.EventImagePushed))
	assert.False(t, handler.CanHandle(domain.EventConfigReload))
	assert.False(t, handler.CanHandle(domain.EventManualReload))
}

func TestImagePushedHandler_Handle_DeploysMatchingRoutes(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	// FindRoutesByImage returns matching routes directly
	configSvc.EXPECT().FindRoutesByImage(mock.Anything, "myapp:latest").Return([]domain.Route{
		{Domain: "app1.example.com", Image: "myapp:latest"},
		{Domain: "app2.example.com", Image: "myapp:latest"},
	})

	// Expect Deploy to be called for both matching routes
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app1.example.com",
		Image:  "myapp:latest",
	}).Return(&domain.Container{ID: "container-1"}, nil)

	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app2.example.com",
		Image:  "myapp:latest",
	}).Return(&domain.Container{ID: "container-2"}, nil)

	event := domain.Event{
		ID:        "event-123",
		Type:      domain.EventImagePushed,
		ImageName: "myapp",
		Tag:       "latest",
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestImagePushedHandler_Handle_NoMatchingRoutes(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	// FindRoutesByImage returns no matching routes
	configSvc.EXPECT().FindRoutesByImage(mock.Anything, "myapp:latest").Return([]domain.Route{})

	// No Deploy calls expected

	event := domain.Event{
		ID:        "event-123",
		Type:      domain.EventImagePushed,
		ImageName: "myapp",
		Tag:       "latest",
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestImagePushedHandler_Handle_EmptyImageName(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	event := domain.Event{
		ID:        "event-123",
		Type:      domain.EventImagePushed,
		ImageName: "",
		Tag:       "latest",
	}

	err := handler.Handle(context.Background(), event)

	assert.ErrorIs(t, err, domain.ErrInvalidImageFormat)
}

func TestImagePushedHandler_Handle_DefaultTag(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	// Image name "myapp" with empty tag should become "myapp:latest"
	configSvc.EXPECT().FindRoutesByImage(mock.Anything, "myapp:latest").Return([]domain.Route{
		{Domain: "app.example.com", Image: "myapp:latest"},
	})

	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:latest",
	}).Return(&domain.Container{ID: "container-1"}, nil)

	event := domain.Event{
		ID:        "event-123",
		Type:      domain.EventImagePushed,
		ImageName: "myapp",
		Tag:       "", // Empty tag should default to "latest"
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestImagePushedHandler_Handle_DeployError(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	configSvc.EXPECT().FindRoutesByImage(mock.Anything, "myapp:latest").Return([]domain.Route{
		{Domain: "app.example.com", Image: "myapp:latest"},
	})

	containerSvc.EXPECT().Deploy(mock.Anything, mock.Anything).Return(nil, errors.New("deploy failed"))

	event := domain.Event{
		ID:        "event-123",
		Type:      domain.EventImagePushed,
		ImageName: "myapp",
		Tag:       "latest",
	}

	// Handler logs error but doesn't fail
	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestImagePushedHandler_Handle_StripsRegistryDomain(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	// FindRoutesByImage handles registry domain stripping internally
	configSvc.EXPECT().FindRoutesByImage(mock.Anything, "docker.io/library/nginx:latest").Return([]domain.Route{
		{Domain: "app.example.com", Image: "registry.example.com/docker.io/library/nginx:latest"},
	})

	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "registry.example.com/docker.io/library/nginx:latest",
	}).Return(&domain.Container{ID: "container-1"}, nil)

	event := domain.Event{
		ID:        "event-123",
		Type:      domain.EventImagePushed,
		ImageName: "docker.io/library/nginx",
		Tag:       "latest",
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

// ConfigReloadHandler tests

func TestConfigReloadHandler_CanHandle(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewConfigReloadHandler(testCtx(), containerSvc, configSvc)

	assert.True(t, handler.CanHandle(domain.EventConfigReload))
	assert.False(t, handler.CanHandle(domain.EventImagePushed))
	assert.False(t, handler.CanHandle(domain.EventManualReload))
}

func TestConfigReloadHandler_Handle_DeploysNewRoutes(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewConfigReloadHandler(testCtx(), containerSvc, configSvc)

	// SyncContainers is called first
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)

	// No existing containers
	containerSvc.EXPECT().List(mock.Anything).Return(map[string]*domain.Container{})

	// New route in config
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "newapp.example.com", Image: "newapp:latest"},
	})

	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "newapp.example.com",
		Image:  "newapp:latest",
	}).Return(&domain.Container{ID: "container-1"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventConfigReload,
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestConfigReloadHandler_Handle_StopsRemovedRoutes(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewConfigReloadHandler(testCtx(), containerSvc, configSvc)

	// SyncContainers is called first
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)

	// Existing container for a route that's no longer configured
	containerSvc.EXPECT().List(mock.Anything).Return(map[string]*domain.Container{
		"removed.example.com": {
			ID: "container-old",
			Labels: map[string]string{
				"gordon.route": "removed.example.com",
				"gordon.image": "oldapp:latest",
			},
		},
	})

	// Empty routes - the route was removed
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{})

	containerSvc.EXPECT().Stop(mock.Anything, "container-old").Return(nil)
	containerSvc.EXPECT().Remove(mock.Anything, "container-old", true).Return(nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventConfigReload,
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestConfigReloadHandler_Handle_RedeploysChangedImage(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewConfigReloadHandler(testCtx(), containerSvc, configSvc)

	// SyncContainers is called first
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)

	// Existing container with old image
	containerSvc.EXPECT().List(mock.Anything).Return(map[string]*domain.Container{
		"app.example.com": {
			ID: "container-1",
			Labels: map[string]string{
				"gordon.route": "app.example.com",
				"gordon.image": "myapp:v1",
			},
		},
	})

	// Config now has different image
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "myapp:v2"},
	})

	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:v2",
	}).Return(&domain.Container{ID: "container-2"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventConfigReload,
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestConfigReloadHandler_Handle_NoChanges(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewConfigReloadHandler(testCtx(), containerSvc, configSvc)

	// SyncContainers is called first
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)

	// Existing container matches config
	containerSvc.EXPECT().List(mock.Anything).Return(map[string]*domain.Container{
		"app.example.com": {
			ID: "container-1",
			Labels: map[string]string{
				"gordon.route": "app.example.com",
				"gordon.image": "myapp:latest",
			},
		},
	})

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "myapp:latest"},
	})

	// No Deploy, Stop, or Remove calls expected

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventConfigReload,
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

// ManualReloadHandler tests

func TestManualReloadHandler_CanHandle(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualReloadHandler(testCtx(), containerSvc, configSvc)

	assert.True(t, handler.CanHandle(domain.EventManualReload))
	assert.False(t, handler.CanHandle(domain.EventImagePushed))
	assert.False(t, handler.CanHandle(domain.EventConfigReload))
}

func TestManualReloadHandler_Handle_StartsOnlyMissingContainers(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualReloadHandler(testCtx(), containerSvc, configSvc)

	// SyncContainers is called first
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)

	// Routes in config
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app1.example.com", Image: "app1:latest"},
		{Domain: "app2.example.com", Image: "app2:latest"},
	})

	// app1 has a running container, app2 does not
	containerSvc.EXPECT().Get(mock.Anything, "app1.example.com").Return(&domain.Container{ID: "container-1"}, true)
	containerSvc.EXPECT().Get(mock.Anything, "app2.example.com").Return(nil, false)

	// Only app2 should be deployed (app1 is already running - never restart healthy containers)
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app2.example.com",
		Image:  "app2:latest",
	}).Return(&domain.Container{ID: "container-2"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualReload,
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestManualReloadHandler_Handle_StartsMissingContainer(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualReloadHandler(testCtx(), containerSvc, configSvc)

	// SyncContainers is called first
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "app:latest"},
	})

	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(nil, false)

	// Deploy should be called to start the missing container
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "app:latest",
	}).Return(&domain.Container{ID: "container-1"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualReload,
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestManualReloadHandler_Handle_DeployErrors(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualReloadHandler(testCtx(), containerSvc, configSvc)

	// SyncContainers is called first
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app1.example.com", Image: "app1:latest"},
		{Domain: "app2.example.com", Image: "app2:latest"},
	})

	// Both containers are missing
	containerSvc.EXPECT().Get(mock.Anything, "app1.example.com").Return(nil, false)
	containerSvc.EXPECT().Get(mock.Anything, "app2.example.com").Return(nil, false)

	// app1 fails to deploy, app2 succeeds
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app1.example.com",
		Image:  "app1:latest",
	}).Return(nil, errors.New("deploy failed"))

	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app2.example.com",
		Image:  "app2:latest",
	}).Return(&domain.Container{ID: "container-2"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualReload,
	}

	err := handler.Handle(context.Background(), event)

	// Should return error indicating some failures
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "1 errors")
}

func TestManualReloadHandler_Handle_DoesNotRestartRunningContainers(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualReloadHandler(testCtx(), containerSvc, configSvc)

	// SyncContainers is called first
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)

	// Route with running container
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "app:latest"},
	})

	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(&domain.Container{ID: "container-1"}, true)

	// NO Deploy call expected - container is already running, never restart healthy containers

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualReload,
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

// ManualDeployHandler tests

func TestManualDeployHandler_CanHandle(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualDeployHandler(testCtx(), containerSvc, configSvc)

	assert.True(t, handler.CanHandle(domain.EventManualDeploy))
	assert.False(t, handler.CanHandle(domain.EventImagePushed))
	assert.False(t, handler.CanHandle(domain.EventManualReload))
}

func TestManualDeployHandler_Handle_DeploysSpecificRoute(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualDeployHandler(testCtx(), containerSvc, configSvc)

	// Configure routes
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app1.example.com", Image: "app1:latest"},
		{Domain: "app2.example.com", Image: "app2:latest"},
	})

	// Only the requested route should be deployed
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app1.example.com",
		Image:  "app1:latest",
	}).Return(&domain.Container{ID: "container-1"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualDeploy,
		Data: &domain.ManualDeployPayload{Domain: "app1.example.com"},
	}

	err := handler.Handle(context.Background(), event)

	assert.NoError(t, err)
}

func TestManualDeployHandler_Handle_RouteNotFound(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualDeployHandler(testCtx(), containerSvc, configSvc)

	// No matching routes
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "other.example.com", Image: "other:latest"},
	})

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualDeploy,
		Data: &domain.ManualDeployPayload{Domain: "unknown.example.com"},
	}

	err := handler.Handle(context.Background(), event)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "route not found")
}

func TestManualDeployHandler_Handle_InvalidPayload(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualDeployHandler(testCtx(), containerSvc, configSvc)

	// Event with nil payload
	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualDeploy,
		Data: nil,
	}

	err := handler.Handle(context.Background(), event)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid manual deploy payload")
}

func TestManualDeployHandler_Handle_EmptyDomain(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualDeployHandler(testCtx(), containerSvc, configSvc)

	// Event with empty domain
	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualDeploy,
		Data: &domain.ManualDeployPayload{Domain: ""},
	}

	err := handler.Handle(context.Background(), event)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid manual deploy payload")
}

func TestManualDeployHandler_Handle_DeployError(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualDeployHandler(testCtx(), containerSvc, configSvc)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "app:latest"},
	})

	containerSvc.EXPECT().Deploy(mock.Anything, mock.Anything).Return(nil, errors.New("deploy failed"))

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualDeploy,
		Data: &domain.ManualDeployPayload{Domain: "app.example.com"},
	}

	err := handler.Handle(context.Background(), event)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to deploy")
}
