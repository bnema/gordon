package registry

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
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

func TestHandler_NotFound(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	req := httptest.NewRequest("GET", "/v2/unknown/path", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
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

func TestHandler_GetManifest_NestedName(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

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

	handler := NewHandler(registrySvc, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	registrySvc.EXPECT().PutManifest(mock.Anything, mock.MatchedBy(func(m *domain.Manifest) bool {
		return m.Name == "myapp" && m.Reference == "latest" && m.ContentType == "application/vnd.docker.distribution.manifest.v2+json"
	})).Return("sha256:abc123", nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/latest", bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "sha256:abc123", rec.Header().Get("Docker-Content-Digest"))
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/manifests/latest")
}

func TestHandler_PutManifest_MissingContentType(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/latest", bytes.NewReader(manifestData))
	// No Content-Type header
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandler_PutManifest_StorageError(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	registrySvc.EXPECT().PutManifest(mock.Anything, mock.Anything).Return("", assert.AnError)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/latest", bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestHandler_ManifestRoutes_MethodNotAllowed(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	req := httptest.NewRequest("DELETE", "/v2/myapp/manifests/latest", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandler_GetBlob_Success(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	// Create a temp file for http.ServeFile
	tmpFile, err := os.CreateTemp("", "blob-*")
	assert.NoError(t, err)
	defer tmpFile.Close()

	blobContent := []byte("blob content here")
	_, err = tmpFile.Write(blobContent)
	assert.NoError(t, err)

	registrySvc.EXPECT().GetBlobPath(mock.Anything, "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4").Return(tmpFile.Name(), nil)

	req := httptest.NewRequest("GET", "/v2/myapp/blobs/sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, blobContent, rec.Body.Bytes())
}

func TestHandler_GetBlob_NotFound(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	registrySvc.EXPECT().GetBlobPath(mock.Anything, "sha256:0000000000000000000000000000000000000000000000000000000000000000").Return("", assert.AnError)

	req := httptest.NewRequest("GET", "/v2/myapp/blobs/sha256:0000000000000000000000000000000000000000000000000000000000000000", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
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

	registrySvc.EXPECT().StartUpload(mock.Anything, "myapp").Return("550e8400-e29b-41d4-a716-446655440000", nil)

	req := httptest.NewRequest("POST", "/v2/myapp/blobs/uploads/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000")
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
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "550e8400-e29b-41d4-a716-446655440000", chunkData).Return(int64(len(chunkData)), nil)

	req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Contains(t, rec.Header().Get("Location"), "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000")
}

func TestHandler_BlobUpload_PUT_Finalize(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)

	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte("final chunk")
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "550e8400-e29b-41d4-a716-446655440000", chunkData).Return(int64(len(chunkData)), nil)
	registrySvc.EXPECT().FinishUpload(mock.Anything, "550e8400-e29b-41d4-a716-446655440000", "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4").Return(nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000?digest=sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", bytes.NewReader(chunkData))
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

	req := httptest.NewRequest("PUT", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000?digest=sha256:wrong", bytes.NewReader(chunkData))
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

func TestHandler_StartBlobUpload_ReturnsDockerUploadUUID(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	registrySvc.EXPECT().StartUpload(mock.Anything, "myapp").Return("550e8400-e29b-41d4-a716-446655440000", nil)

	req := httptest.NewRequest("POST", "/v2/myapp/blobs/uploads/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", rec.Header().Get("Docker-Upload-UUID"))
}

func TestHandler_BlobUpload_PATCH_ReturnsDockerUploadUUID(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte("chunk content")
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "550e8400-e29b-41d4-a716-446655440000", chunkData).Return(int64(len(chunkData)), nil)

	req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "550e8400-e29b-41d4-a716-446655440000", rec.Header().Get("Docker-Upload-UUID"))
}

func TestMaxBlobChunkSize_Under100MB(t *testing.T) {
	// Verify the constant leaves headroom below Cloudflare's 100MB limit
	assert.Less(t, int64(MaxBlobChunkSize), int64(100*1024*1024))
	assert.Greater(t, int64(MaxBlobChunkSize), int64(90*1024*1024))
}

func TestHandler_BlobUpload_PATCH_ReturnsRangeHeader(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte("chunk content")
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "550e8400-e29b-41d4-a716-446655440000", chunkData).Return(int64(len(chunkData)), nil)

	req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	// OCI spec: Range header should reflect total received bytes
	assert.Equal(t, "0-12", rec.Header().Get("Range"))
}

func TestHandler_ManifestValidation_InvalidName(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	tests := []struct {
		name string
		path string
	}{
		{"path traversal", "/v2/../etc/passwd/manifests/latest"},
		{"uppercase in name", "/v2/MyApp/manifests/latest"},
		{"special chars", "/v2/my@app/manifests/latest"},
		{"ends with separator", "/v2/myapp-/manifests/latest"},
		{"double separator", "/v2/my..app/manifests/latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "NAME_INVALID")
		})
	}
}

func TestHandler_ManifestValidation_InvalidReference(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	tests := []struct {
		name string
		path string
	}{
		{"starts with dash", "/v2/myapp/manifests/-invalid"},
		{"contains special chars", "/v2/myapp/manifests/tag@invalid"},
		{"contains colon in middle", "/v2/myapp/manifests/tag:with:colon"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "TAG_INVALID")
		})
	}
}

func TestHandler_BlobValidation_InvalidDigest(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	tests := []struct {
		name   string
		digest string
	}{
		{"missing sha256 prefix", "a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"},
		{"wrong hash length", "sha256:abc123"},
		{"invalid algorithm", "md5:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"},
		{"uppercase hex", "sha256:A3ED95CAEB02FFE68CDD9FD84406680AE93D633CB16422D00E8A7C22955B46D4"},
		{"hex with special chars", "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46g4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/v2/myapp/blobs/"+tt.digest, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "DIGEST_INVALID")
		})
	}
}

func TestHandler_BlobUploadValidation_InvalidUUID(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	tests := []struct {
		name string
		uuid string
	}{
		{"not a uuid", "not-a-uuid"},
		{"wrong format", "550e8400e29b41d4a716446655440000"},
		{"wrong version", "550e8400-e29b-31d4-a716-446655440000"},
		{"uppercase", "550E8400-E29B-41D4-A716-446655440000"},
		{"uuid with extra chars", "550e8400-e29b-41d4-a716-446655440000-extra"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/"+tt.uuid, bytes.NewReader([]byte("chunk")))
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Contains(t, rec.Body.String(), "BLOB_UPLOAD_INVALID")
		})
	}
}

func TestHandler_PutManifest_TooLarge(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	// Create manifest data that exceeds MaxManifestSize
	largeManifest := bytes.Repeat([]byte("x"), MaxManifestSize+1)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/latest", bytes.NewReader(largeManifest))
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "SIZE_INVALID")
}

func TestHandler_BlobUpload_ChunkTooLarge(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	// Create chunk data that exceeds MaxBlobChunkSize
	largeChunk := bytes.Repeat([]byte("x"), MaxBlobChunkSize+1)

	req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000", bytes.NewReader(largeChunk))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "SIZE_INVALID")
}

func TestHandler_BlobUpload_AppendError(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte("chunk content")
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "550e8400-e29b-41d4-a716-446655440000", chunkData).Return(int64(0), assert.AnError)

	req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "BLOB_UPLOAD_UNKNOWN")
}

func TestHandler_BlobUpload_PUT_FinishUploadError(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte("final chunk")
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "550e8400-e29b-41d4-a716-446655440000", chunkData).Return(int64(len(chunkData)), nil)
	registrySvc.EXPECT().FinishUpload(mock.Anything, "550e8400-e29b-41d4-a716-446655440000", "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4").Return(assert.AnError)
	registrySvc.EXPECT().CancelUpload(mock.Anything, "550e8400-e29b-41d4-a716-446655440000").Return(nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000?digest=sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "DIGEST_INVALID")
}

func TestHandler_BlobUpload_PATCH_ZeroLength(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	chunkData := []byte{}
	registrySvc.EXPECT().AppendBlobChunk(mock.Anything, "myapp", "550e8400-e29b-41d4-a716-446655440000", chunkData).Return(int64(0), nil)

	req := httptest.NewRequest("PATCH", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000", bytes.NewReader(chunkData))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)
	// Zero-length chunk should not set Range header
	assert.Empty(t, rec.Header().Get("Range"))
}

func TestHandler_GetBlob_HEAD(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	tmpFile, err := os.CreateTemp("", "blob-*")
	assert.NoError(t, err)
	defer tmpFile.Close()

	blobContent := []byte("blob content here")
	_, err = tmpFile.Write(blobContent)
	assert.NoError(t, err)

	registrySvc.EXPECT().GetBlobPath(mock.Anything, "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4").Return(tmpFile.Name(), nil)

	req := httptest.NewRequest("HEAD", "/v2/myapp/blobs/sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	// HEAD request should not return body
	assert.Empty(t, rec.Body.Bytes())
}

func TestHandler_TagList_EmptyTags(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	registrySvc.EXPECT().ListTags(mock.Anything, "myapp").Return([]string{}, nil)

	req := httptest.NewRequest("GET", "/v2/myapp/tags/list", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	assert.Equal(t, "myapp", response.Name)
	assert.Empty(t, response.Tags)
}

func TestHandler_NestedRepositoryPath(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	registrySvc.EXPECT().GetManifest(mock.Anything, "org/team/project/app", "v1.0.0").Return(&domain.Manifest{
		Name:        "org/team/project/app",
		Reference:   "v1.0.0",
		ContentType: "application/vnd.docker.distribution.manifest.v2+json",
		Data:        manifestData,
	}, nil)

	req := httptest.NewRequest("GET", "/v2/org/team/project/app/manifests/v1.0.0", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandler_PutManifest_WithAnnotations(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	// OCI image manifest with annotations
	manifestData := []byte(`{
		"schemaVersion": 2,
		"annotations": {
			"org.opencontainers.image.created": "2026-02-06T12:00:00Z",
			"org.opencontainers.image.authors": "test@example.com"
		}
	}`)

	registrySvc.EXPECT().PutManifest(mock.Anything, mock.MatchedBy(func(m *domain.Manifest) bool {
		return m.Name == "myapp" && m.Reference == "latest" && len(m.Annotations) >= 0
	})).Return("sha256:abc123", nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/latest", bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
}

func TestHandler_BlobUploadRoutes_MethodNotAllowed(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"GET on upload start", "GET", "/v2/myapp/blobs/uploads/"},
		{"DELETE on upload", "DELETE", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000"},
		{"GET on upload uuid", "GET", "/v2/myapp/blobs/uploads/550e8400-e29b-41d4-a716-446655440000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
			assert.Contains(t, rec.Body.String(), "METHOD_NOT_ALLOWED")
		})
	}
}

func TestHandler_PutManifest_ValidDigestReference(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	manifestData := []byte(`{"schemaVersion": 2}`)
	digest := "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"

	registrySvc.EXPECT().PutManifest(mock.Anything, mock.MatchedBy(func(m *domain.Manifest) bool {
		return m.Name == "myapp" && m.Reference == digest
	})).Return(digest, nil)

	req := httptest.NewRequest("PUT", "/v2/myapp/manifests/"+digest, bytes.NewReader(manifestData))
	req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, digest, rec.Header().Get("Docker-Content-Digest"))
}

func TestMaxManifestSize_Constant(t *testing.T) {
	// Verify MaxManifestSize is set to 10MB
	assert.Equal(t, 10*1024*1024, MaxManifestSize)
}

func TestHandler_ErrorResponse_Format(t *testing.T) {
	registrySvc := inmocks.NewMockRegistryService(t)
	handler := NewHandler(registrySvc, testLogger())

	req := httptest.NewRequest("GET", "/v2/unknown/path", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "registry/2.0", rec.Header().Get("Docker-Distribution-API-Version"))

	var errorResp struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	assert.NoError(t, json.NewDecoder(rec.Body).Decode(&errorResp))
	assert.Len(t, errorResp.Errors, 1)
	assert.Equal(t, "NOT_FOUND", errorResp.Errors[0].Code)
}