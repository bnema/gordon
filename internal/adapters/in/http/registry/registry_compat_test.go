package registry

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestRegistryHTTPCompatibilityContract(t *testing.T) {
	const (
		name       = "library/app"
		reference  = "latest"
		mediaType  = "application/vnd.oci.image.manifest.v1+json"
		digest     = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		uploadUUID = "550e8400-e29b-41d4-a716-446655440000"
	)
	manifestData := []byte(`{"schemaVersion":2}`)

	t.Run("/v2/ base", func(t *testing.T) {
		h := NewHandler(inmocks.NewMockRegistryService(t), testLogger(), 8)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/", nil))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "registry/2.0", rec.Header().Get("Docker-Distribution-API-Version"))
	})

	t.Run("manifest GET and PUT preserve headers", func(t *testing.T) {
		svc := inmocks.NewMockRegistryService(t)
		h := NewHandler(svc, testLogger(), 8)
		svc.EXPECT().GetManifest(mock.Anything, name, reference).Return(&domain.Manifest{Name: name, Reference: reference, ContentType: mediaType, Data: manifestData}, nil)
		getRec := httptest.NewRecorder()
		h.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/v2/library/app/manifests/latest", nil))
		assert.Equal(t, http.StatusOK, getRec.Code)
		assert.Equal(t, mediaType, getRec.Header().Get("Content-Type"))
		assert.Equal(t, "19", getRec.Header().Get("Content-Length"))
		assert.Equal(t, manifestData, getRec.Body.Bytes())

		svc.EXPECT().PutManifest(mock.Anything, mock.MatchedBy(func(m *domain.Manifest) bool {
			return m.Name == name && m.Reference == reference && m.ContentType == mediaType && string(m.Data) == string(manifestData)
		})).Return(digest, nil)
		putReq := httptest.NewRequest(http.MethodPut, "/v2/library/app/manifests/latest", bytes.NewReader(manifestData))
		putReq.Header.Set("Content-Type", mediaType)
		putRec := httptest.NewRecorder()
		h.ServeHTTP(putRec, putReq)
		assert.Equal(t, http.StatusCreated, putRec.Code)
		assert.Equal(t, digest, putRec.Header().Get("Docker-Content-Digest"))
		assert.Equal(t, "/v2/library/app/manifests/latest", putRec.Header().Get("Location"))
	})

	t.Run("blob upload start patch finish and blob GET", func(t *testing.T) {
		svc := inmocks.NewMockRegistryService(t)
		h := NewHandler(svc, testLogger(), 16)
		svc.EXPECT().StartUpload(mock.Anything, name).Return(uploadUUID, nil)
		startRec := httptest.NewRecorder()
		h.ServeHTTP(startRec, httptest.NewRequest(http.MethodPost, "/v2/library/app/blobs/uploads/", nil))
		assert.Equal(t, http.StatusAccepted, startRec.Code)
		assert.Equal(t, "/v2/library/app/blobs/uploads/"+uploadUUID, startRec.Header().Get("Location"))
		assert.Equal(t, "0-0", startRec.Header().Get("Range"))
		assert.Equal(t, uploadUUID, startRec.Header().Get("Docker-Upload-UUID"))

		svc.EXPECT().AppendBlobChunk(mock.Anything, name, uploadUUID, mock.Anything, int64(5), int64(DefaultMaxBlobSize)).Return(int64(5), nil).Once()
		patchReq := httptest.NewRequest(http.MethodPatch, "/v2/library/app/blobs/uploads/"+uploadUUID, strings.NewReader("hello"))
		patchReq.ContentLength = 5
		patchRec := httptest.NewRecorder()
		h.ServeHTTP(patchRec, patchReq)
		assert.Equal(t, http.StatusAccepted, patchRec.Code)
		assert.Equal(t, "0-4", patchRec.Header().Get("Range"))

		svc.EXPECT().AppendBlobChunk(mock.Anything, name, uploadUUID, mock.Anything, int64(0), int64(DefaultMaxBlobSize)).Return(int64(5), nil).Once()
		svc.EXPECT().FinishUpload(mock.Anything, uploadUUID, digest).Return(nil)
		finishRec := httptest.NewRecorder()
		h.ServeHTTP(finishRec, httptest.NewRequest(http.MethodPut, "/v2/library/app/blobs/uploads/"+uploadUUID+"?digest="+digest, nil))
		assert.Equal(t, http.StatusCreated, finishRec.Code)
		assert.Equal(t, "/v2/library/app/blobs/"+digest, finishRec.Header().Get("Location"))
		assert.Equal(t, digest, finishRec.Header().Get("Docker-Content-Digest"))

		blobFile := t.TempDir() + "/blob"
		require.NoError(t, os.WriteFile(blobFile, []byte("blob"), 0o600))
		svc.EXPECT().GetBlobPath(mock.Anything, digest).Return(blobFile, nil)
		blobRec := httptest.NewRecorder()
		h.ServeHTTP(blobRec, httptest.NewRequest(http.MethodGet, "/v2/library/app/blobs/"+digest, nil))
		assert.Equal(t, http.StatusOK, blobRec.Code)
		assert.Equal(t, "text/plain; charset=utf-8", blobRec.Header().Get("Content-Type"))
		assert.Equal(t, []byte("blob"), blobRec.Body.Bytes())
	})

	t.Run("tags list", func(t *testing.T) {
		svc := inmocks.NewMockRegistryService(t)
		h := NewHandler(svc, testLogger(), 8)
		svc.EXPECT().ListTags(mock.Anything, name).Return([]string{"latest", "v1"}, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v2/library/app/tags/list", nil))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
		assert.JSONEq(t, `{"name":"library/app","tags":["latest","v1"]}`, rec.Body.String())
	})

	t.Run("invalid name and reference", func(t *testing.T) {
		h := NewHandler(inmocks.NewMockRegistryService(t), testLogger(), 8)
		badName := httptest.NewRecorder()
		h.ServeHTTP(badName, httptest.NewRequest(http.MethodGet, "/v2/../app/manifests/latest", nil))
		assert.Equal(t, http.StatusBadRequest, badName.Code)
		assert.Contains(t, badName.Body.String(), "NAME_INVALID")
		badRef := httptest.NewRecorder()
		h.ServeHTTP(badRef, httptest.NewRequest(http.MethodGet, "/v2/library/app/manifests/bad@tag", nil))
		assert.Equal(t, http.StatusBadRequest, badRef.Code)
		assert.Contains(t, badRef.Body.String(), "TAG_INVALID")
	})

	t.Run("upload too large cancels and returns registry error", func(t *testing.T) {
		svc := inmocks.NewMockRegistryService(t)
		h := NewHandler(svc, testLogger(), 4)
		svc.EXPECT().AppendBlobChunk(mock.Anything, name, uploadUUID, mock.Anything, int64(5), int64(DefaultMaxBlobSize)).Return(int64(0), domain.ErrBlobSizeExceeded)
		svc.EXPECT().CancelUpload(mock.Anything, uploadUUID).Return(nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/v2/library/app/blobs/uploads/"+uploadUUID, strings.NewReader("12345"))
		req.ContentLength = 5
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Contains(t, rec.Body.String(), "SIZE_INVALID")
	})
}
