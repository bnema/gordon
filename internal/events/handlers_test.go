package events

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"gordon/internal/config"
	"gordon/pkg/runtime"
)

// MockContainerManager is a mock implementation of container.ManagerInterface
type MockContainerManager struct {
	mock.Mock
}

func (m *MockContainerManager) DeployContainer(ctx context.Context, route config.Route) (*runtime.Container, error) {
	args := m.Called(ctx, route)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*runtime.Container), args.Error(1)
}

func (m *MockContainerManager) StopContainer(ctx context.Context, containerID string) error {
	args := m.Called(ctx, containerID)
	return args.Error(0)
}

func (m *MockContainerManager) RemoveContainer(ctx context.Context, containerID string, force bool) error {
	args := m.Called(ctx, containerID, force)
	return args.Error(0)
}

func (m *MockContainerManager) ListContainers() map[string]*runtime.Container {
	args := m.Called()
	if args.Get(0) == nil {
		return nil
	}
	return args.Get(0).(map[string]*runtime.Container)
}

func (m *MockContainerManager) GetContainer(domain string) (*runtime.Container, bool) {
	args := m.Called(domain)
	if args.Get(0) == nil {
		return nil, args.Bool(1)
	}
	return args.Get(0).(*runtime.Container), args.Bool(1)
}

func (m *MockContainerManager) StopContainerByDomain(ctx context.Context, domain string) error {
	args := m.Called(ctx, domain)
	return args.Error(0)
}

func (m *MockContainerManager) Runtime() runtime.Runtime {
	args := m.Called()
	return args.Get(0).(runtime.Runtime)
}

func (m *MockContainerManager) HealthCheck(ctx context.Context) map[string]bool {
	args := m.Called(ctx)
	return args.Get(0).(map[string]bool)
}

func (m *MockContainerManager) SyncContainers(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockContainerManager) GetContainerPort(ctx context.Context, domain string, internalPort int) (int, error) {
	args := m.Called(ctx, domain, internalPort)
	return args.Int(0), args.Error(1)
}

func (m *MockContainerManager) GetNetworkForApp(domain string) string {
	args := m.Called(domain)
	return args.String(0)
}

func (m *MockContainerManager) AutoStartContainers(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockContainerManager) StopAllManagedContainers(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func TestContainerEventHandler_CanHandle(t *testing.T) {
	handler := &ContainerEventHandler{}
	
	testCases := []struct {
		eventType EventType
		expected  bool
	}{
		{ImagePushed, true},
		{ConfigReload, true},
		{ManualReload, true},
		{ContainerStop, true},
		{ContainerStart, true},
		{ImageDeleted, false},
		{ContainerHealthCheck, false},
	}
	
	for _, tc := range testCases {
		t.Run(string(tc.eventType), func(t *testing.T) {
			result := handler.CanHandle(tc.eventType)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestContainerEventHandler_Handle_ImagePushed(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: map[string]string{
			"app.example.com": "nginx:latest",
		},
	}
	
	handler := &ContainerEventHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	// Mock container listing (empty list initially)
	mockManager.On("ListContainers").Return(map[string]*runtime.Container{})
	
	// Mock successful deployment
	expectedRoute := config.Route{
		Domain: "app.example.com",
		Image:  "nginx:latest",
		HTTPS:  true,
	}
	mockContainer := &runtime.Container{
		ID:   "container123",
		Name: "gordon-app-example-com",
	}
	mockManager.On("DeployContainer", mock.Anything, expectedRoute).Return(mockContainer, nil)
	
	event := Event{
		Type:      ImagePushed,
		ImageName: "nginx",
		Tag:       "latest",
	}
	
	err := handler.Handle(event)
	
	assert.NoError(t, err)
	mockManager.AssertExpectations(t)
}

func TestContainerEventHandler_Handle_ImagePushed_NoMatchingRoutes(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: map[string]string{
			"app.example.com": "postgres:14", // Different image
		},
	}
	
	handler := &ContainerEventHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	event := Event{
		Type:      ImagePushed,
		ImageName: "nginx",
		Tag:       "latest",
	}
	
	err := handler.Handle(event)
	
	// Should succeed but not deploy anything
	assert.NoError(t, err)
	mockManager.AssertExpectations(t) // No calls expected
}

func TestContainerEventHandler_Handle_ImagePushed_MultipleRoutes(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: map[string]string{
			"app1.example.com": "nginx:latest",
			"app2.example.com": "nginx:latest",
			"app3.example.com": "postgres:14", // Different image
		},
	}
	
	handler := &ContainerEventHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	// Mock successful deployments for matching routes
	mockManager.On("ListContainers").Return(map[string]*runtime.Container{}).Times(2)
	
	expectedRoute1 := config.Route{Domain: "app1.example.com", Image: "nginx:latest", HTTPS: true}
	expectedRoute2 := config.Route{Domain: "app2.example.com", Image: "nginx:latest", HTTPS: true}
	
	mockContainer1 := &runtime.Container{ID: "container1", Name: "gordon-app1-example-com"}
	mockContainer2 := &runtime.Container{ID: "container2", Name: "gordon-app2-example-com"}
	
	mockManager.On("DeployContainer", mock.Anything, expectedRoute1).Return(mockContainer1, nil)
	mockManager.On("DeployContainer", mock.Anything, expectedRoute2).Return(mockContainer2, nil)
	
	event := Event{
		Type:      ImagePushed,
		ImageName: "nginx",
		Tag:       "latest",
	}
	
	err := handler.Handle(event)
	
	assert.NoError(t, err)
	mockManager.AssertExpectations(t)
}

func TestContainerEventHandler_Handle_ConfigReload(t *testing.T) {
	mockManager := &MockContainerManager{}
	
	// Mock existing containers
	existingContainers := map[string]*runtime.Container{
		"old.example.com": {
			ID:   "container1",
			Name: "old-container",
			Labels: map[string]string{
				"gordon.route": "old.example.com",
				"gordon.image": "old:image",
			},
		},
		"app.example.com": {
			ID:   "container2", 
			Name: "app-container",
			Labels: map[string]string{
				"gordon.route": "app.example.com",
				"gordon.image": "nginx:latest",
			},
		},
	}
	
	// New config with different routes
	newConfig := &config.Config{
		Routes: map[string]string{
			"app.example.com": "nginx:latest", // Keep this one
			"new.example.com": "postgres:14",  // Add new one
			// Remove old.example.com
		},
	}
	
	handler := &ContainerEventHandler{
		manager: mockManager,
		config:  newConfig,
	}
	
	// Mock manager calls
	mockManager.On("ListContainers").Return(existingContainers).Once() // First call in handleConfigReload
	mockManager.On("StopContainer", mock.Anything, "container1").Return(nil) // Stop removed container
	mockManager.On("RemoveContainer", mock.Anything, "container1", true).Return(nil) // Remove removed container
	
	// Mock deployment for new route
	expectedRoute := config.Route{Domain: "new.example.com", Image: "postgres:14", HTTPS: true}
	mockContainer := &runtime.Container{ID: "container3", Name: "gordon-new-example-com"}
	mockManager.On("ListContainers").Return(map[string]*runtime.Container{}).Once() // Second call in deployContainerForRoute
	mockManager.On("DeployContainer", mock.Anything, expectedRoute).Return(mockContainer, nil)
	
	event := Event{
		Type: ConfigReload,
	}
	
	err := handler.Handle(event)
	
	assert.NoError(t, err)
	mockManager.AssertExpectations(t)
}

func TestContainerEventHandler_Handle_ManualReload(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: map[string]string{
			"app.example.com": "nginx:latest",
		},
	}
	
	handler := &ContainerEventHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	// Mock GetContainer to return true (container exists)
	mockContainer := &runtime.Container{ID: "container1", Name: "app-container"}
	mockManager.On("GetContainer", "app.example.com").Return(mockContainer, true)
	
	// Mock deployment
	expectedRoute := config.Route{Domain: "app.example.com", Image: "nginx:latest", HTTPS: true}
	mockManager.On("DeployContainer", mock.Anything, expectedRoute).Return(mockContainer, nil)
	
	event := Event{
		Type: ManualReload,
	}
	
	err := handler.Handle(event)
	
	assert.NoError(t, err)
	mockManager.AssertExpectations(t)
}

func TestContainerEventHandler_Handle_ContainerStop(t *testing.T) {
	mockManager := &MockContainerManager{}
	
	handler := &ContainerEventHandler{
		manager: mockManager,
	}
	
	// Mock container stop
	mockManager.On("StopContainer", mock.Anything, "container123").Return(nil)
	
	event := Event{
		Type:        ContainerStop,
		ContainerID: "container123",
	}
	
	err := handler.Handle(event)
	
	assert.NoError(t, err)
	mockManager.AssertExpectations(t)
}

func TestContainerEventHandler_Handle_ContainerStart(t *testing.T) {
	handler := &ContainerEventHandler{}
	
	event := Event{
		Type:        ContainerStart,
		ContainerID: "container123",
	}
	
	err := handler.Handle(event)
	
	// Should return error as start is not implemented
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "container start not implemented")
}

func TestContainerEventHandler_Handle_UnsupportedEvent(t *testing.T) {
	handler := &ContainerEventHandler{}
	
	event := Event{
		Type: ImageDeleted, // Unsupported event type
	}
	
	err := handler.Handle(event)
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported event type")
}

func TestAutoRouteHandler_CanHandle(t *testing.T) {
	handler := &AutoRouteHandler{}
	
	testCases := []struct {
		eventType EventType
		expected  bool
	}{
		{ImagePushed, true},
		{ImageDeleted, false},
		{ConfigReload, false},
		{ManualReload, false},
		{ContainerStop, false},
		{ContainerStart, false},
		{ContainerHealthCheck, false},
	}
	
	for _, tc := range testCases {
		t.Run(string(tc.eventType), func(t *testing.T) {
			result := handler.CanHandle(tc.eventType)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestAutoRouteHandler_Handle_AutoRouteEnabled(t *testing.T) {
	mockManager := &MockContainerManager{}
	
	// Create a temporary config file for testing
	tempConfigFile := "/tmp/test-gordon-config.toml"
	configContent := `[server]
registry_domain = "registry.example.com"

[auto_route]
enabled = true

[routes]
`
	err := os.WriteFile(tempConfigFile, []byte(configContent), 0644)
	require.NoError(t, err)
	defer os.Remove(tempConfigFile)
	
	// Load config from temp file to set viper config file path
	viper.SetConfigFile(tempConfigFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	mockConfig := &config.Config{
		AutoRoute: config.AutoRouteConfig{
			Enabled: true,
		},
		Routes: make(map[string]string),
		Server: config.ServerConfig{
			RegistryDomain: "registry.example.com",
		},
	}
	
	handler := &AutoRouteHandler{
		config:  mockConfig,
		manager: mockManager,
	}
	
	payload := ImagePushedPayload{
		Name:      "myapp.example.com",
		Reference: "latest",
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err = handler.Handle(event)
	
	assert.NoError(t, err)
	// Check that route was added to config
	assert.Equal(t, "registry.example.com/myapp.example.com:latest", mockConfig.Routes["myapp.example.com"])
	mockManager.AssertExpectations(t)
}

func TestAutoRouteHandler_Handle_AutoRouteDisabled(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		AutoRoute: config.AutoRouteConfig{
			Enabled: false,
		},
		Routes: make(map[string]string),
	}
	
	handler := &AutoRouteHandler{
		config:  mockConfig,
		manager: mockManager,
	}
	
	payload := ImagePushedPayload{
		Name:      "myapp.example.com",
		Reference: "latest",
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err := handler.Handle(event)
	
	// Should succeed but not create route
	assert.NoError(t, err)
	assert.Empty(t, mockConfig.Routes)
	mockManager.AssertExpectations(t) // No calls expected
}

func TestAutoRouteHandler_Handle_RouteAlreadyExists(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		AutoRoute: config.AutoRouteConfig{
			Enabled: true,
		},
		Routes: map[string]string{
			"myapp.example.com": "existing:image",
		},
	}
	
	handler := &AutoRouteHandler{
		config:  mockConfig,
		manager: mockManager,
	}
	
	payload := ImagePushedPayload{
		Name:      "myapp.example.com",
		Reference: "latest",
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err := handler.Handle(event)
	
	// Should succeed but not modify existing route
	assert.NoError(t, err)
	assert.Equal(t, "existing:image", mockConfig.Routes["myapp.example.com"])
	mockManager.AssertExpectations(t) // No calls expected
}

func TestAutoRouteHandler_Handle_InvalidImageName(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		AutoRoute: config.AutoRouteConfig{
			Enabled: true,
		},
		Routes: make(map[string]string),
	}
	
	handler := &AutoRouteHandler{
		config:  mockConfig,
		manager: mockManager,
	}
	
	payload := ImagePushedPayload{
		Name:      "nginx", // No domain extractable
		Reference: "latest",
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err := handler.Handle(event)
	
	// Should succeed but not create route
	assert.NoError(t, err)
	assert.Empty(t, mockConfig.Routes)
	mockManager.AssertExpectations(t) // No calls expected
}

func TestNewContainerEventHandler(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{}
	
	handler := NewContainerEventHandler(mockManager, mockConfig)
	
	require.NotNil(t, handler)
	assert.Equal(t, mockManager, handler.manager)
	assert.Equal(t, mockConfig, handler.config)
}

func TestNewAutoRouteHandler(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{}
	
	handler := NewAutoRouteHandler(mockConfig, mockManager)
	
	require.NotNil(t, handler)
	assert.Equal(t, mockManager, handler.manager)
	assert.Equal(t, mockConfig, handler.config)
}

func TestVersionHandler_CanHandle(t *testing.T) {
	handler := &VersionHandler{}
	
	testCases := []struct {
		eventType EventType
		expected  bool
	}{
		{ImagePushed, true},
		{ImageDeleted, false},
		{ConfigReload, false},
		{ManualReload, false},
		{ContainerStop, false},
		{ContainerStart, false},
		{ContainerHealthCheck, false},
	}
	
	for _, tc := range testCases {
		t.Run(string(tc.eventType), func(t *testing.T) {
			result := handler.CanHandle(tc.eventType)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestVersionHandler_Handle_VersionedDeployment(t *testing.T) {
	// Create a temporary config file for testing
	tempConfigFile := "/tmp/test-gordon-version-config.toml"
	configContent := `[server]
registry_domain = "registry.example.com"

[routes]
"app.example.com" = "myapp:v1.0"
`
	err := os.WriteFile(tempConfigFile, []byte(configContent), 0644)
	require.NoError(t, err)
	defer os.Remove(tempConfigFile)
	
	// Load config from temp file to set viper config file path
	viper.SetConfigFile(tempConfigFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: map[string]string{
			"app.example.com": "myapp:v2.0", // Match the payload reference so it finds routes
		},
	}
	
	handler := &VersionHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	// Mock deployment
	expectedRoute := config.Route{Domain: "app.example.com", Image: "myapp:v2.0", HTTPS: true}
	mockContainer := &runtime.Container{ID: "container1", Name: "app-container"}
	mockManager.On("DeployContainer", mock.Anything, expectedRoute).Return(mockContainer, nil)
	
	payload := ImagePushedPayload{
		Name:      "myapp",
		Reference: "v2.0",
		Annotations: map[string]string{
			"version": "v2.0",
		},
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err = handler.Handle(event)
	
	assert.NoError(t, err)
	// Check that route was updated to new version
	assert.Equal(t, "myapp:v2.0", mockConfig.Routes["app.example.com"])
	mockManager.AssertExpectations(t)
}

func TestVersionHandler_Handle_NonVersionedDeployment(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: make(map[string]string),
	}
	
	handler := &VersionHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	payload := ImagePushedPayload{
		Name:        "myapp",
		Reference:   "latest",
		Annotations: map[string]string{}, // No version annotations
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err := handler.Handle(event)
	
	// Should succeed but not process anything
	assert.NoError(t, err)
	assert.Empty(t, mockConfig.Routes)
	mockManager.AssertExpectations(t) // No calls expected
}

func TestVersionHandler_Handle_NoMatchingRoutes(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: map[string]string{
			"app.example.com": "nginx:latest", // Different image
		},
	}
	
	handler := &VersionHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	payload := ImagePushedPayload{
		Name:      "myapp",
		Reference: "v2.0",
		Annotations: map[string]string{
			"version": "v2.0",
		},
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err := handler.Handle(event)
	
	// Should succeed but not deploy anything
	assert.NoError(t, err)
	assert.Equal(t, "nginx:latest", mockConfig.Routes["app.example.com"])
	mockManager.AssertExpectations(t) // No calls expected
}

func TestVersionHandler_Handle_InvalidPayload(t *testing.T) {
	handler := &VersionHandler{}
	
	event := Event{
		Type: ImagePushed,
		Data: "invalid payload",
	}
	
	err := handler.Handle(event)
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid payload type")
}

func TestVersionHandler_Handle_WrongEventType(t *testing.T) {
	handler := &VersionHandler{}
	
	event := Event{
		Type: ConfigReload,
	}
	
	err := handler.Handle(event)
	
	assert.NoError(t, err) // Should succeed but do nothing
}

func TestVersionHandler_Handle_DeploymentFailure(t *testing.T) {
	// Create a temporary config file for testing
	tempConfigFile := "/tmp/test-gordon-version-fail-config.toml"
	configContent := `[server]
registry_domain = "registry.example.com"

[routes]
"app.example.com" = "myapp:v1.0"
`
	err := os.WriteFile(tempConfigFile, []byte(configContent), 0644)
	require.NoError(t, err)
	defer os.Remove(tempConfigFile)
	
	// Load config from temp file to set viper config file path
	viper.SetConfigFile(tempConfigFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: map[string]string{
			"app.example.com": "myapp:v2.0", // Match the payload reference so it finds routes
		},
	}
	
	handler := &VersionHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	// Mock deployment failure
	expectedRoute := config.Route{Domain: "app.example.com", Image: "myapp:v2.0", HTTPS: true}
	mockManager.On("DeployContainer", mock.Anything, expectedRoute).Return(nil, fmt.Errorf("deployment failed"))
	
	payload := ImagePushedPayload{
		Name:      "myapp",
		Reference: "v2.0",
		Annotations: map[string]string{
			"version": "v2.0",
		},
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err = handler.Handle(event)
	
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "version deployment completed with")
	mockManager.AssertExpectations(t)
}

func TestVersionHandler_Handle_FullImageReference(t *testing.T) {
	// Create a temporary config file for testing
	tempConfigFile := "/tmp/test-gordon-version-full-config.toml"
	configContent := `[server]
registry_domain = "registry.example.com"

[routes]
"app.example.com" = "myapp:v1.0"
`
	err := os.WriteFile(tempConfigFile, []byte(configContent), 0644)
	require.NoError(t, err)
	defer os.Remove(tempConfigFile)
	
	// Load config from temp file to set viper config file path
	viper.SetConfigFile(tempConfigFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{
		Routes: map[string]string{
			"app.example.com": "myapp:v2.0", // Match the payload reference so it finds routes
		},
	}
	
	handler := &VersionHandler{
		manager: mockManager,
		config:  mockConfig,
	}
	
	// Mock deployment with full image reference
	expectedRoute := config.Route{Domain: "app.example.com", Image: "registry.example.com/myapp:v2.0", HTTPS: true}
	mockContainer := &runtime.Container{ID: "container1", Name: "app-container"}
	mockManager.On("DeployContainer", mock.Anything, expectedRoute).Return(mockContainer, nil)
	
	payload := ImagePushedPayload{
		Name:      "myapp",
		Reference: "v2.0",
		Annotations: map[string]string{
			"version": "registry.example.com/myapp:v2.0", // Full image reference
		},
	}
	
	event := Event{
		Type: ImagePushed,
		Data: payload,
	}
	
	err = handler.Handle(event)
	
	assert.NoError(t, err)
	// Check that route was updated to full image reference
	assert.Equal(t, "registry.example.com/myapp:v2.0", mockConfig.Routes["app.example.com"])
	mockManager.AssertExpectations(t)
}

func TestNewVersionHandler(t *testing.T) {
	mockManager := &MockContainerManager{}
	mockConfig := &config.Config{}
	
	handler := NewVersionHandler(mockManager, mockConfig)
	
	require.NotNil(t, handler)
	assert.Equal(t, mockManager, handler.manager)
	assert.Equal(t, mockConfig, handler.config)
}