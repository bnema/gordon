package container

import (
	"context"
	"errors"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "gordon/internal/boundaries/in/mocks"
	"gordon/internal/domain"
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

	// Configure routes that match the pushed image
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app1.example.com", Image: "myapp:latest"},
		{Domain: "app2.example.com", Image: "myapp:latest"},
		{Domain: "other.example.com", Image: "otherapp:latest"},
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

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestImagePushedHandler_Handle_NoMatchingRoutes(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "other.example.com", Image: "otherapp:latest"},
	})

	// No Deploy calls expected

	event := domain.Event{
		ID:        "event-123",
		Type:      domain.EventImagePushed,
		ImageName: "myapp",
		Tag:       "latest",
	}

	err := handler.Handle(event)

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

	err := handler.Handle(event)

	assert.ErrorIs(t, err, domain.ErrInvalidImageFormat)
}

func TestImagePushedHandler_Handle_DefaultTag(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	// Image name "myapp" with empty tag should become "myapp:latest"
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
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

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestImagePushedHandler_Handle_DeployError(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewImagePushedHandler(testCtx(), containerSvc, configSvc)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
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
	err := handler.Handle(event)

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

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestConfigReloadHandler_Handle_StopsRemovedRoutes(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewConfigReloadHandler(testCtx(), containerSvc, configSvc)

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

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestConfigReloadHandler_Handle_RedeploysChangedImage(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewConfigReloadHandler(testCtx(), containerSvc, configSvc)

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

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestConfigReloadHandler_Handle_NoChanges(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewConfigReloadHandler(testCtx(), containerSvc, configSvc)

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

	err := handler.Handle(event)

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

func TestManualReloadHandler_Handle_RedeploysExistingContainers(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualReloadHandler(testCtx(), containerSvc, configSvc)

	// Routes in config
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app1.example.com", Image: "app1:latest"},
		{Domain: "app2.example.com", Image: "app2:latest"},
	})

	// Only app1 has a running container
	containerSvc.EXPECT().Get(mock.Anything, "app1.example.com").Return(&domain.Container{ID: "container-1"}, true)
	containerSvc.EXPECT().Get(mock.Anything, "app2.example.com").Return(nil, false)

	// Only app1 should be redeployed (app2 has no container)
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app1.example.com",
		Image:  "app1:latest",
	}).Return(&domain.Container{ID: "container-1-new"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualReload,
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestManualReloadHandler_Handle_NoContainersRunning(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualReloadHandler(testCtx(), containerSvc, configSvc)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "app:latest"},
	})

	containerSvc.EXPECT().Get(mock.Anything, "app.example.com").Return(nil, false)

	// No Deploy calls - no container exists to reload

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualReload,
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestManualReloadHandler_Handle_DeployErrors(t *testing.T) {
	containerSvc := inmocks.NewMockContainerService(t)
	configSvc := inmocks.NewMockConfigService(t)

	handler := NewManualReloadHandler(testCtx(), containerSvc, configSvc)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app1.example.com", Image: "app1:latest"},
		{Domain: "app2.example.com", Image: "app2:latest"},
	})

	containerSvc.EXPECT().Get(mock.Anything, "app1.example.com").Return(&domain.Container{ID: "container-1"}, true)
	containerSvc.EXPECT().Get(mock.Anything, "app2.example.com").Return(&domain.Container{ID: "container-2"}, true)

	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app1.example.com",
		Image:  "app1:latest",
	}).Return(nil, errors.New("deploy failed"))

	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app2.example.com",
		Image:  "app2:latest",
	}).Return(&domain.Container{ID: "container-2-new"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventManualReload,
	}

	err := handler.Handle(event)

	// Should return error indicating some failures
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "1 errors")
}
