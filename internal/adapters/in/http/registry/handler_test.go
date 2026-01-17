package registry

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "gordon/internal/boundaries/in/mocks"
	"gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestHandler_Base(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	req := httptest.NewRequest("GET", "/v2/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "registry/2.0", rec.Header().Get("Docker-Distribution-API-Version"))
}

func TestHandler_GetManifest_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	registrySvc.EXPECT().GetManifest(mock.Anything, "myapp", "latest").Return(&domain.Manifest{
		Name:        "myapp",
		Reference:   "latest",
		ContentType: "application/vnd.docker.distribution.manifest.v2+json",
		Data:        manifestData,
	}, nil)

	req := httptest.NewRequest("GET", "/v2/myapp/manifests/latest", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/vnd.docker.distribution.manifest.v2+json", rec.Header().Get("Content-Type"))
	assert.Equal(t, manifestData, rec.Body.Bytes())
}

func TestHandler_GetManifest_HEAD(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	registrySvc.EXPECT().GetManifest(mock.Anything, "myapp", "latest").Return(&domain.Manifest{
		Name:        "myapp",
		Reference:   "latest",
		ContentType: "application/vnd.docker.distribution.manifest.v2+json",
		Data:        manifestData,
	}, nil)

	req := httptest.NewRequest("HEAD", "/v2/myapp/manifests/latest", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/vnd.docker.distribution.manifest.v2+json", rec.Header().Get("Content-Type"))
	// HEAD should not return body
	assert.Empty(t, rec.Body.Bytes())
}

func TestHandler_GetManifest_NotFound(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	registrySvc.EXPECT().GetManifest(mock.Anything, "myapp", "notexists").Return(nil, assert.AnError)

	req := httptest.NewRequest("GET", "/v2/myapp/manifests/notexists", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestHandler_BlobRoutes_MethodNotAllowed(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	req := httptest.NewRequest("POST", "/v2/myapp/blobs/sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestHandler_StartBlobUpload_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	registrySvc.EXPECT().StartUpload(mock.Anything, "myapp").Return("1234567890-myapp", nil)

	req := httptest.NewRequest("POST", "/v2/myapp/blobs/uploads/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/blobs/uploads/1234567890-myapp")
	assert.Equal(t, "0-0", rec.Header().Get("Range"))
}

func TestHandler_StartBlobUpload_Error(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	registrySvc.EXPECT().StartUpload(mock.Anything, "myapp").Return("", assert.AnError)

	req := httptest.NewRequest("POST", "/v2/myapp/blobs/uploads/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestHandler_BlobUpload_PATCH(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte("chunk content")
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "1234567890-myapp", chunkData).Return(int64(len(chunkData)), nil)

	req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/1234567890-myapp", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/blobs/uploads/1234567890-myapp")
}

func TestHandler_BlobUpload_PUT_Finalize(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte("final chunk")
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "1234567890-myapp", chunkData).Return(int64(len(chunkData)), nil)
	registrySvc.EXPECT().FinishUpload(mock.Anything, "1234567890-myapp", "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4").Return(nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/blobs/uploads/1234567890-myapp?digest=sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/blobs/sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4")
	assert.Equal(t, "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", rec.Header().Get("Docker-Content-Digest"))
}

func TestHandler_BlobUpload_PUT_DigestMismatch(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte("final chunk")
	// Note: Invalid digest format is rejected by validation before reaching storage layer
	// This test verifies that invalid digests are properly rejected

	req := httptest.NewRequest("PUT", "/v2/myapp/blobs/uploads/1234567890-myapp?digest=sha256:wrong", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestHandler_ListTags_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	registrySvc.EXPECT().ListTags(mock.Anything, "myapp").Return([]string{"latest", "v1.0", "v2.0"}, nil)

	req := httptest.NewRequest("GET", "/v2/myapp/tags/list", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var response struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "myapp", response.Name)
	assert.Contains(t, response.Tags, "latest")
}

func TestHandler_ListTags_NotFound(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	registrySvc.EXPECT().ListTags(mock.Anything, "notexists").Return(nil, assert.AnError)

	req := httptest.NewRequest("GET", "/v2/notexists/tags/list", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestHandler_ListTags_MethodNotAllowed(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	req := httptest.NewRequest("POST", "/v2/myapp/tags/list", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

func TestHandler_RegisterRoutes(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Verify routes are registered by making a request through the mux
	req := httptest.NewRequest("GET", "/v2/", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
