package registry

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "gordon/internal/boundaries/in/mocks"
	"gordon/internal/boundaries/out/mocks"
	"gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestHandler_Base(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	req := httptest.NewRequest("GET", "/v2/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "registry/2.0", rec.Header().Get("Docker-Distribution-API-Version"))
}

func TestHandler_NotFound(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	req := httptest.NewRequest("GET", "/v2/unknown/path", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_GetManifest_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

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
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

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
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	registrySvc.EXPECT().GetManifest(mock.Anything, "myapp", "notexists").Return(nil, assert.AnError)

	req := httptest.NewRequest("GET", "/v2/myapp/manifests/notexists", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_GetManifest_NestedName(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	registrySvc.EXPECT().GetManifest(mock.Anything, "org/project/app", "v1.0").Return(&domain.Manifest{
		Name:        "org/project/app",
		Reference:   "v1.0",
		ContentType: "application/vnd.docker.distribution.manifest.v2+json",
		Data:        manifestData,
	}, nil)

	req := httptest.NewRequest("GET", "/v2/org/project/app/manifests/v1.0", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_PutManifest_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	registrySvc.EXPECT().PutManifest(mock.Anything, mock.MatchedBy(func(m *domain.Manifest) bool {
		return m.Name == "myapp" && m.Reference == "latest" && m.ContentType == "application/vnd.docker.distribution.manifest.v2+json"
	})).Return(nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/latest", bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Docker-Content-Digest"))
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/manifests/latest")
}

func TestHandler_PutManifest_MissingContentType(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/latest", bytes.NewReader(manifestData))
	// No Content-Type header
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_PutManifest_StorageError(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	registrySvc.EXPECT().PutManifest(mock.Anything, mock.Anything).Return(assert.AnError)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/latest", bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandler_ManifestRoutes_MethodNotAllowed(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	req := httptest.NewRequest("DELETE", "/v2/myapp/manifests/latest", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandler_GetBlob_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	// Create a temp file for http.ServeFile
	tmpFile, err := os.CreateTemp("", "blob-*")
	assert.NoError(t, err)
	defer tmpFile.Close()

	blobContent := []byte("blob content here")
	_, err = tmpFile.Write(blobContent)
	assert.NoError(t, err)

	blobStorage.EXPECT().GetBlobPath("sha256:abc123").Return(tmpFile.Name(), nil)

	req := httptest.NewRequest("GET", "/v2/myapp/blobs/sha256:abc123", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, blobContent, rec.Body.Bytes())
}

func TestHandler_GetBlob_NotFound(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	blobStorage.EXPECT().GetBlobPath("sha256:notexists").Return("", assert.AnError)

	req := httptest.NewRequest("GET", "/v2/myapp/blobs/sha256:notexists", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_BlobRoutes_MethodNotAllowed(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	req := httptest.NewRequest("POST", "/v2/myapp/blobs/sha256:abc123", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandler_StartBlobUpload_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	registrySvc.EXPECT().StartUpload(mock.Anything, "myapp").Return("upload-uuid-123", nil)

	req := httptest.NewRequest("POST", "/v2/myapp/blobs/uploads/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/blobs/uploads/upload-uuid-123")
	assert.Equal(t, "0-0", rec.Header().Get("Range"))
}

func TestHandler_StartBlobUpload_Error(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	registrySvc.EXPECT().StartUpload(mock.Anything, "myapp").Return("", assert.AnError)

	req := httptest.NewRequest("POST", "/v2/myapp/blobs/uploads/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandler_BlobUpload_PATCH(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	chunkData := []byte("chunk content")
	blobStorage.EXPECT().AppendBlobChunk("myapp", "upload-uuid-123", chunkData).Return(int64(len(chunkData)), nil)

	req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/upload-uuid-123", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/blobs/uploads/upload-uuid-123")
}

func TestHandler_BlobUpload_PUT_Finalize(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	chunkData := []byte("final chunk")
	blobStorage.EXPECT().AppendBlobChunk("myapp", "upload-uuid-123", chunkData).Return(int64(len(chunkData)), nil)
	registrySvc.EXPECT().FinishUpload(mock.Anything, "upload-uuid-123", "sha256:abc123").Return(nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/blobs/uploads/upload-uuid-123?digest=sha256:abc123", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/blobs/sha256:abc123")
	assert.Equal(t, "sha256:abc123", rec.Header().Get("Docker-Content-Digest"))
}

func TestHandler_BlobUpload_PUT_DigestMismatch(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	chunkData := []byte("final chunk")
	blobStorage.EXPECT().AppendBlobChunk("myapp", "upload-uuid-123", chunkData).Return(int64(len(chunkData)), nil)
	registrySvc.EXPECT().FinishUpload(mock.Anything, "upload-uuid-123", "sha256:wrong").Return(assert.AnError)
	registrySvc.EXPECT().CancelUpload(mock.Anything, "upload-uuid-123").Return(nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/blobs/uploads/upload-uuid-123?digest=sha256:wrong", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_ListTags_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	registrySvc.EXPECT().ListTags(mock.Anything, "myapp").Return([]string{"latest", "v1.0", "v2.0"}, nil)

	req := httptest.NewRequest("GET", "/v2/myapp/tags/list", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "myapp")
	assert.Contains(t, rec.Body.String(), "latest")
}

func TestHandler_ListTags_NotFound(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	registrySvc.EXPECT().ListTags(mock.Anything, "notexists").Return(nil, assert.AnError)

	req := httptest.NewRequest("GET", "/v2/notexists/tags/list", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_ListTags_MethodNotAllowed(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	req := httptest.NewRequest("POST", "/v2/myapp/tags/list", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandler_RegisterRoutes(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	handler := NewHandler(registrySvc, blobStorage, eventBus, testLogger())

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	// Verify routes are registered by making a request through the mux
	req := httptest.NewRequest("GET", "/v2/", nil)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
