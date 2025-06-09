package registry

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"gordon/internal/config"
	"gordon/internal/events"
	registryMocks "gordon/internal/registry/mocks"
)

// MockEventBus is a mock implementation of events.EventBus
type MockEventBus struct {
	mock.Mock
}

func (m *MockEventBus) Publish(eventType events.EventType, payload interface{}) error {
	args := m.Called(eventType, payload)
	return args.Error(0)
}

func (m *MockEventBus) Subscribe(handler events.EventHandler) error {
	args := m.Called(handler)
	return args.Error(0)
}

func (m *MockEventBus) Unsubscribe(handler events.EventHandler) error {
	args := m.Called(handler)
	return args.Error(0)
}

func (m *MockEventBus) Start() error {
	args := m.Called()
	return args.Error(0)
}

func (m *MockEventBus) Stop() error {
	args := m.Called()
	return args.Error(0)
}

func TestNewServer(t *testing.T) {
	mockEventBus := &MockEventBus{}
	cfg := &config.Config{
		Server: config.ServerConfig{
			DataDir: t.TempDir(),
		},
	}

	server, err := NewServer(cfg, mockEventBus)

	require.NoError(t, err)
	assert.NotNil(t, server)
	assert.Equal(t, cfg, server.config)
	assert.Equal(t, mockEventBus, server.eventBus)
	assert.NotNil(t, server.storage)
	assert.NotNil(t, server.mux)
}

func TestNewServer_StorageInitError(t *testing.T) {
	mockEventBus := &MockEventBus{}
	cfg := &config.Config{
		Server: config.ServerConfig{
			DataDir: "/invalid/path/that/cannot/be/created",
		},
	}

	server, err := NewServer(cfg, mockEventBus)

	assert.Error(t, err)
	assert.Nil(t, server)
	assert.Contains(t, err.Error(), "failed to initialize storage")
}

func TestServer_HandleBase(t *testing.T) {
	server := createTestServer(t)

	req := httptest.NewRequest("GET", "/v2/", nil)
	w := httptest.NewRecorder()

	server.handleBase(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "registry/2.0", w.Header().Get("Docker-Distribution-API-Version"))
}

func TestServer_HandleGetManifest_Success(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	reference := "latest"
	manifestData := []byte(`{"test": "manifest"}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	mockStorage.On("GetManifest", name, reference).Return(manifestData, contentType, nil)

	req := httptest.NewRequest("GET", fmt.Sprintf("/v2/%s/manifests/%s", name, reference), nil)
	req.SetPathValue("name", name)
	req.SetPathValue("reference", reference)
	w := httptest.NewRecorder()

	server.handleGetManifest(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, contentType, w.Header().Get("Content-Type"))
	assert.Equal(t, fmt.Sprintf("%d", len(manifestData)), w.Header().Get("Content-Length"))
	assert.Equal(t, manifestData, w.Body.Bytes())
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleGetManifest_HEAD(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	reference := "latest"
	manifestData := []byte(`{"test": "manifest"}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	mockStorage.On("GetManifest", name, reference).Return(manifestData, contentType, nil)

	req := httptest.NewRequest("HEAD", fmt.Sprintf("/v2/%s/manifests/%s", name, reference), nil)
	req.SetPathValue("name", name)
	req.SetPathValue("reference", reference)
	w := httptest.NewRecorder()

	server.handleGetManifest(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, contentType, w.Header().Get("Content-Type"))
	assert.Equal(t, fmt.Sprintf("%d", len(manifestData)), w.Header().Get("Content-Length"))
	assert.Empty(t, w.Body.Bytes()) // HEAD should not return body
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleGetManifest_NotFound(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "nonexistent"
	reference := "latest"

	mockStorage.On("GetManifest", name, reference).Return(nil, "", fmt.Errorf("manifest not found"))

	req := httptest.NewRequest("GET", fmt.Sprintf("/v2/%s/manifests/%s", name, reference), nil)
	req.SetPathValue("name", name)
	req.SetPathValue("reference", reference)
	w := httptest.NewRecorder()

	server.handleGetManifest(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestServer_HandlePutManifest_Success(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)
	mockEventBus := server.eventBus.(*MockEventBus)

	name := "myapp"
	reference := "latest"
	manifestData := []byte(`{"test": "manifest"}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	mockStorage.On("PutManifest", name, reference, contentType, manifestData).Return(nil)
	mockEventBus.On("Publish", events.ImagePushed, mock.MatchedBy(func(payload events.ImagePushedPayload) bool {
		return payload.Name == name && payload.Reference == reference && bytes.Equal(payload.Manifest, manifestData)
	})).Return(nil)

	req := httptest.NewRequest("PUT", fmt.Sprintf("/v2/%s/manifests/%s", name, reference), bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", contentType)
	req.SetPathValue("name", name)
	req.SetPathValue("reference", reference)
	w := httptest.NewRecorder()

	server.handlePutManifest(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Contains(t, w.Header().Get("Docker-Content-Digest"), "sha256:")
	assert.Equal(t, fmt.Sprintf("/v2/%s/manifests/%s", name, reference), w.Header().Get("Location"))
	mockStorage.AssertExpectations(t)
	mockEventBus.AssertExpectations(t)
}

func TestServer_HandlePutManifest_NoContentType(t *testing.T) {
	server := createTestServerWithMockStorage(t)

	name := "myapp"
	reference := "latest"
	manifestData := []byte(`{"test": "manifest"}`)

	req := httptest.NewRequest("PUT", fmt.Sprintf("/v2/%s/manifests/%s", name, reference), bytes.NewReader(manifestData))
	// No Content-Type header
	req.SetPathValue("name", name)
	req.SetPathValue("reference", reference)
	w := httptest.NewRecorder()

	server.handlePutManifest(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "Content-Type header required")
}

func TestServer_HandlePutManifest_StorageError(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	reference := "latest"
	manifestData := []byte(`{"test": "manifest"}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	mockStorage.On("PutManifest", name, reference, contentType, manifestData).Return(fmt.Errorf("storage error"))

	req := httptest.NewRequest("PUT", fmt.Sprintf("/v2/%s/manifests/%s", name, reference), bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", contentType)
	req.SetPathValue("name", name)
	req.SetPathValue("reference", reference)
	w := httptest.NewRecorder()

	server.handlePutManifest(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestServer_HandlePutManifest_WithAnnotations(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)
	mockEventBus := server.eventBus.(*MockEventBus)

	name := "myapp"
	reference := "latest"
	// OCI manifest with annotations
	manifestData := []byte(`{
		"schemaVersion": 2,
		"mediaType": "application/vnd.oci.image.manifest.v1+json",
		"annotations": {
			"version": "v1.0",
			"description": "Test image"
		}
	}`)
	contentType := "application/vnd.oci.image.manifest.v1+json"

	mockStorage.On("PutManifest", name, reference, contentType, manifestData).Return(nil)
	mockEventBus.On("Publish", events.ImagePushed, mock.MatchedBy(func(payload events.ImagePushedPayload) bool {
		return payload.Name == name && 
			payload.Reference == reference && 
			len(payload.Annotations) == 2 &&
			payload.Annotations["version"] == "v1.0"
	})).Return(nil)

	req := httptest.NewRequest("PUT", fmt.Sprintf("/v2/%s/manifests/%s", name, reference), bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", contentType)
	req.SetPathValue("name", name)
	req.SetPathValue("reference", reference)
	w := httptest.NewRecorder()

	server.handlePutManifest(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	mockStorage.AssertExpectations(t)
	mockEventBus.AssertExpectations(t)
}

func TestServer_HandleGetBlob_Success(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	digest := "sha256:abc123"
	blobPath := "/tmp/test-blob"

	// Create test blob file
	testContent := "test blob content"
	err := os.WriteFile(blobPath, []byte(testContent), 0644)
	require.NoError(t, err)
	defer os.Remove(blobPath)

	mockStorage.On("GetBlobPath", digest).Return(blobPath, nil)

	req := httptest.NewRequest("GET", fmt.Sprintf("/v2/%s/blobs/%s", name, digest), nil)
	req.SetPathValue("name", name)
	req.SetPathValue("digest", digest)
	w := httptest.NewRecorder()

	server.handleGetBlob(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, testContent, w.Body.String())
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleGetBlob_NotFound(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	digest := "sha256:nonexistent"

	mockStorage.On("GetBlobPath", digest).Return("", fmt.Errorf("blob not found"))

	req := httptest.NewRequest("GET", fmt.Sprintf("/v2/%s/blobs/%s", name, digest), nil)
	req.SetPathValue("name", name)
	req.SetPathValue("digest", digest)
	w := httptest.NewRecorder()

	server.handleGetBlob(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleStartBlobUpload_Success(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	uuid := "test-upload-uuid"

	mockStorage.On("StartBlobUpload", name).Return(uuid, nil)

	req := httptest.NewRequest("POST", fmt.Sprintf("/v2/%s/blobs/uploads/", name), nil)
	req.SetPathValue("name", name)
	w := httptest.NewRecorder()

	server.handleStartBlobUpload(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid), w.Header().Get("Location"))
	assert.Equal(t, "0-0", w.Header().Get("Range"))
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleStartBlobUpload_Error(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"

	mockStorage.On("StartBlobUpload", name).Return("", fmt.Errorf("storage error"))

	req := httptest.NewRequest("POST", fmt.Sprintf("/v2/%s/blobs/uploads/", name), nil)
	req.SetPathValue("name", name)
	w := httptest.NewRecorder()

	server.handleStartBlobUpload(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleBlobUpload_PATCH(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	uuid := "test-upload-uuid"
	chunk := []byte("chunk data")

	mockStorage.On("AppendBlobChunk", name, uuid, chunk).Return(int64(len(chunk)), nil)

	req := httptest.NewRequest("PATCH", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid), bytes.NewReader(chunk))
	req.SetPathValue("name", name)
	req.SetPathValue("uuid", uuid)
	w := httptest.NewRecorder()

	server.handleBlobUpload(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid), w.Header().Get("Location"))
	assert.Equal(t, fmt.Sprintf("0-%d", len(chunk)-1), w.Header().Get("Range"))
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleBlobUpload_PUT_Success(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	uuid := "test-upload-uuid"
	digest := "sha256:abc123"
	chunk := []byte("final chunk")

	mockStorage.On("AppendBlobChunk", name, uuid, chunk).Return(int64(len(chunk)), nil)
	mockStorage.On("FinishBlobUpload", uuid, digest).Return(nil)

	req := httptest.NewRequest("PUT", fmt.Sprintf("/v2/%s/blobs/uploads/%s?digest=%s", name, uuid, digest), bytes.NewReader(chunk))
	req.SetPathValue("name", name)
	req.SetPathValue("uuid", uuid)
	w := httptest.NewRecorder()

	server.handleBlobUpload(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, fmt.Sprintf("/v2/%s/blobs/%s", name, digest), w.Header().Get("Location"))
	assert.Equal(t, digest, w.Header().Get("Docker-Content-Digest"))
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleBlobUpload_PUT_FinishError(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	uuid := "test-upload-uuid"
	digest := "sha256:abc123"
	chunk := []byte("final chunk")

	mockStorage.On("AppendBlobChunk", name, uuid, chunk).Return(int64(len(chunk)), nil)
	mockStorage.On("FinishBlobUpload", uuid, digest).Return(fmt.Errorf("digest mismatch"))
	mockStorage.On("CancelBlobUpload", uuid).Return(nil)

	req := httptest.NewRequest("PUT", fmt.Sprintf("/v2/%s/blobs/uploads/%s?digest=%s", name, uuid, digest), bytes.NewReader(chunk))
	req.SetPathValue("name", name)
	req.SetPathValue("uuid", uuid)
	w := httptest.NewRecorder()

	server.handleBlobUpload(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "digest mismatch")
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleBlobUpload_AppendError(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	uuid := "test-upload-uuid"
	chunk := []byte("chunk data")

	mockStorage.On("AppendBlobChunk", name, uuid, chunk).Return(int64(0), fmt.Errorf("append error"))

	req := httptest.NewRequest("PATCH", fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, uuid), bytes.NewReader(chunk))
	req.SetPathValue("name", name)
	req.SetPathValue("uuid", uuid)
	w := httptest.NewRecorder()

	server.handleBlobUpload(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleListTags_Success(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "myapp"
	tags := []string{"latest", "v1.0", "stable"}

	mockStorage.On("ListTags", name).Return(tags, nil)

	req := httptest.NewRequest("GET", fmt.Sprintf("/v2/%s/tags/list", name), nil)
	req.SetPathValue("name", name)
	w := httptest.NewRecorder()

	server.handleListTags(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	
	// The server correctly uses JSON marshaling for tags
	expectedResponse := fmt.Sprintf(`{"name":"%s","tags":["latest","v1.0","stable"]}`, name)
	assert.Equal(t, expectedResponse, w.Body.String())
	mockStorage.AssertExpectations(t)
}

func TestServer_HandleListTags_NotFound(t *testing.T) {
	server := createTestServerWithMockStorage(t)
	mockStorage := server.storage.(*registryMocks.MockStorage)

	name := "nonexistent"

	mockStorage.On("ListTags", name).Return(nil, fmt.Errorf("not found"))

	req := httptest.NewRequest("GET", fmt.Sprintf("/v2/%s/tags/list", name), nil)
	req.SetPathValue("name", name)
	w := httptest.NewRecorder()

	server.handleListTags(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	mockStorage.AssertExpectations(t)
}

func TestServer_Start_Integration(t *testing.T) {
	// This is more of an integration test to verify server startup
	server := createTestServer(t)
	server.config.Server.RegistryPort = 0 // Use any available port

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start server in background
	errChan := make(chan error, 1)
	go func() {
		errChan <- server.Start(ctx)
	}()

	// Cancel context to shutdown server
	cancel()

	// Wait for server to shutdown
	select {
	case err := <-errChan:
		assert.NoError(t, err)
	}
}

func TestServer_SetupRoutes(t *testing.T) {
	server := createTestServer(t)

	// Test that routes are properly set up by checking a few key endpoints
	testCases := []struct {
		method       string
		path         string
		expectedCode int
	}{
		{"GET", "/v2/", http.StatusOK},
		{"GET", "/v2/myapp/manifests/latest", http.StatusNotFound}, // Expected since manifest doesn't exist
		{"HEAD", "/v2/myapp/manifests/latest", http.StatusNotFound}, // Expected since manifest doesn't exist
		{"PUT", "/v2/myapp/manifests/latest", http.StatusBadRequest}, // Expected since no Content-Type header
		{"GET", "/v2/myapp/blobs/sha256:abc123", http.StatusNotFound}, // Expected since blob doesn't exist
		{"HEAD", "/v2/myapp/blobs/sha256:abc123", http.StatusNotFound}, // Expected since blob doesn't exist
		{"POST", "/v2/myapp/blobs/uploads/", http.StatusAccepted},
		{"PATCH", "/v2/myapp/blobs/uploads/uuid123", http.StatusInternalServerError}, // Expected since upload doesn't exist
		{"PUT", "/v2/myapp/blobs/uploads/uuid123", http.StatusInternalServerError}, // Expected since upload doesn't exist
		{"GET", "/v2/myapp/tags/list", http.StatusOK}, // Returns empty list when no tags exist
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s %s", tc.method, tc.path), func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			w := httptest.NewRecorder()

			// Set path values for routes that need them
			if strings.Contains(tc.path, "myapp") {
				req.SetPathValue("name", "myapp")
			}
			if strings.Contains(tc.path, "latest") {
				req.SetPathValue("reference", "latest")
			}
			if strings.Contains(tc.path, "sha256:abc123") {
				req.SetPathValue("digest", "sha256:abc123")
			}
			if strings.Contains(tc.path, "uuid123") {
				req.SetPathValue("uuid", "uuid123")
			}

			server.mux.ServeHTTP(w, req)

			// We verify that the route exists and returns the expected status
			assert.Equal(t, tc.expectedCode, w.Code, "Route %s %s should return %d", tc.method, tc.path, tc.expectedCode)
		})
	}
}

// Helper functions

func createTestServer(t *testing.T) *Server {
	mockEventBus := &MockEventBus{}
	cfg := &config.Config{
		Server: config.ServerConfig{
			DataDir:      t.TempDir(),
			RegistryPort: 5000,
		},
		RegistryAuth: config.RegistryAuthConfig{
			Enabled: false,
		},
	}

	server, err := NewServer(cfg, mockEventBus)
	require.NoError(t, err)
	return server
}

func createTestServerWithMockStorage(t *testing.T) *Server {
	mockEventBus := &MockEventBus{}
	mockStorage := &registryMocks.MockStorage{}
	
	cfg := &config.Config{
		Server: config.ServerConfig{
			DataDir:      t.TempDir(),
			RegistryPort: 5000,
		},
		RegistryAuth: config.RegistryAuthConfig{
			Enabled: false,
		},
	}

	server := &Server{
		config:   cfg,
		mux:      http.NewServeMux(),
		storage:  mockStorage,
		eventBus: mockEventBus,
	}
	server.setupRoutes()
	return server
}