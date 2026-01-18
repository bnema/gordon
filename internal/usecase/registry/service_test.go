package registry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func TestService_GetManifest_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifestData := []byte(`{"schemaVersion": 2}`)
	manifestStorage.EXPECT().GetManifest("myapp", "latest").Return(manifestData, "application/vnd.docker.distribution.manifest.v2+json", nil)

	result, err := svc.GetManifest(ctx, "myapp", "latest")

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "myapp", result.Name)
	assert.Equal(t, "latest", result.Reference)
	assert.Equal(t, "application/vnd.docker.distribution.manifest.v2+json", result.ContentType)
	assert.Equal(t, manifestData, result.Data)
}

func TestService_GetManifest_NotFound(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifestStorage.EXPECT().GetManifest("myapp", "notexists").Return(nil, "", errors.New("manifest not found"))

	result, err := svc.GetManifest(ctx, "myapp", "notexists")

	assert.Error(t, err)
	assert.Nil(t, result)
	assert.Contains(t, err.Error(), "failed to get manifest")
}

func TestService_PutManifest_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifest := &domain.Manifest{
		Name:        "myapp",
		Reference:   "latest",
		ContentType: "application/vnd.docker.distribution.manifest.v2+json",
		Data:        []byte(`{"schemaVersion": 2}`),
	}

	manifestStorage.EXPECT().PutManifest("myapp", "latest", "application/vnd.docker.distribution.manifest.v2+json", manifest.Data).Return(nil)
	eventBus.EXPECT().Publish(domain.EventImagePushed, mock.AnythingOfType("domain.ImagePushedPayload")).Return(nil)

	digest, err := svc.PutManifest(ctx, manifest)

	assert.NoError(t, err)
	assert.NotEmpty(t, digest)
	assert.True(t, strings.HasPrefix(digest, "sha256:"))
}

func TestService_PutManifest_StorageError(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifest := &domain.Manifest{
		Name:        "myapp",
		Reference:   "latest",
		ContentType: "application/vnd.docker.distribution.manifest.v2+json",
		Data:        []byte(`{"schemaVersion": 2}`),
	}

	manifestStorage.EXPECT().PutManifest("myapp", "latest", "application/vnd.docker.distribution.manifest.v2+json", manifest.Data).Return(errors.New("storage full"))

	_, err := svc.PutManifest(ctx, manifest)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to store manifest")
}

func TestService_PutManifest_EventPublishError(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifest := &domain.Manifest{
		Name:        "myapp",
		Reference:   "latest",
		ContentType: "application/vnd.docker.distribution.manifest.v2+json",
		Data:        []byte(`{"schemaVersion": 2}`),
	}

	manifestStorage.EXPECT().PutManifest("myapp", "latest", "application/vnd.docker.distribution.manifest.v2+json", manifest.Data).Return(nil)
	eventBus.EXPECT().Publish(domain.EventImagePushed, mock.Anything).Return(errors.New("event bus error"))

	// Should still succeed - event publish error is logged but doesn't fail the operation
	_, err := svc.PutManifest(ctx, manifest)

	assert.NoError(t, err)
}

func TestService_PutManifest_NilEventBus(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)

	// Nil event bus
	svc := NewService(blobStorage, manifestStorage, nil)
	ctx := testContext()

	manifest := &domain.Manifest{
		Name:        "myapp",
		Reference:   "latest",
		ContentType: "application/vnd.docker.distribution.manifest.v2+json",
		Data:        []byte(`{"schemaVersion": 2}`),
	}

	manifestStorage.EXPECT().PutManifest("myapp", "latest", "application/vnd.docker.distribution.manifest.v2+json", manifest.Data).Return(nil)

	_, err := svc.PutManifest(ctx, manifest)

	assert.NoError(t, err)
}

func TestService_DeleteManifest_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifestStorage.EXPECT().DeleteManifest("myapp", "latest").Return(nil)

	err := svc.DeleteManifest(ctx, "myapp", "latest")

	assert.NoError(t, err)
}

func TestService_DeleteManifest_Error(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifestStorage.EXPECT().DeleteManifest("myapp", "latest").Return(errors.New("manifest not found"))

	err := svc.DeleteManifest(ctx, "myapp", "latest")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete manifest")
}

func TestService_GetBlob_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobData := []byte("blob content")
	blobStorage.EXPECT().GetBlob("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4").Return(io.NopCloser(bytes.NewReader(blobData)), nil)

	reader, err := svc.GetBlob(ctx, "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4")

	assert.NoError(t, err)
	assert.NotNil(t, reader)

	data, _ := io.ReadAll(reader)
	assert.Equal(t, blobData, data)
}

func TestService_GetBlob_NotFound(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobStorage.EXPECT().GetBlob("sha256:notexists").Return(nil, errors.New("blob not found"))

	reader, err := svc.GetBlob(ctx, "sha256:notexists")

	assert.Error(t, err)
	assert.Nil(t, reader)
	assert.Contains(t, err.Error(), "failed to get blob")
}

func TestService_PutBlob_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobData := []byte("blob content")
	reader := bytes.NewReader(blobData)

	blobStorage.EXPECT().PutBlob("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", mock.Anything, int64(len(blobData))).Return(nil)

	err := svc.PutBlob(ctx, "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", reader, int64(len(blobData)))

	assert.NoError(t, err)
}

func TestService_PutBlob_Error(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobData := []byte("blob content")
	reader := bytes.NewReader(blobData)

	blobStorage.EXPECT().PutBlob("sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", mock.Anything, int64(len(blobData))).Return(errors.New("storage full"))

	err := svc.PutBlob(ctx, "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4", reader, int64(len(blobData)))

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to store blob")
}

func TestService_BlobExists(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobStorage.EXPECT().BlobExists("sha256:exists").Return(true)
	blobStorage.EXPECT().BlobExists("sha256:notexists").Return(false)

	assert.True(t, svc.BlobExists(ctx, "sha256:exists"))
	assert.False(t, svc.BlobExists(ctx, "sha256:notexists"))
}

func TestService_StartUpload_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobStorage.EXPECT().StartBlobUpload("myapp").Return("1234567890-myapp", nil)

	uuid, err := svc.StartUpload(ctx, "myapp")

	assert.NoError(t, err)
	assert.Equal(t, "1234567890-myapp", uuid)
}

func TestService_StartUpload_Error(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobStorage.EXPECT().StartBlobUpload("myapp").Return("", errors.New("cannot create upload"))

	uuid, err := svc.StartUpload(ctx, "myapp")

	assert.Error(t, err)
	assert.Empty(t, uuid)
	assert.Contains(t, err.Error(), "failed to start blob upload")
}

func TestService_FinishUpload_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobStorage.EXPECT().FinishBlobUpload("1234567890-myapp", "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4").Return(nil)

	err := svc.FinishUpload(ctx, "1234567890-myapp", "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4")

	assert.NoError(t, err)
}

func TestService_FinishUpload_Error(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobStorage.EXPECT().FinishBlobUpload("1234567890-myapp", "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4").Return(errors.New("digest mismatch"))

	err := svc.FinishUpload(ctx, "1234567890-myapp", "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to finish blob upload")
}

func TestService_CancelUpload_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobStorage.EXPECT().CancelBlobUpload("1234567890-myapp").Return(nil)

	err := svc.CancelUpload(ctx, "1234567890-myapp")

	assert.NoError(t, err)
}

func TestService_CancelUpload_Error(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	blobStorage.EXPECT().CancelBlobUpload("1234567890-myapp").Return(errors.New("upload not found"))

	err := svc.CancelUpload(ctx, "1234567890-myapp")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to cancel blob upload")
}

func TestService_ListTags_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifestStorage.EXPECT().ListTags("myapp").Return([]string{"latest", "v1.0", "v2.0"}, nil)

	tags, err := svc.ListTags(ctx, "myapp")

	assert.NoError(t, err)
	assert.Equal(t, []string{"latest", "v1.0", "v2.0"}, tags)
}

func TestService_ListTags_Error(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifestStorage.EXPECT().ListTags("myapp").Return(nil, errors.New("repository not found"))

	tags, err := svc.ListTags(ctx, "myapp")

	assert.Error(t, err)
	assert.Nil(t, tags)
	assert.Contains(t, err.Error(), "failed to list tags")
}

func TestService_ListRepositories_Success(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifestStorage.EXPECT().ListRepositories().Return([]string{"myapp", "otherapp", "thirdapp"}, nil)

	repos, err := svc.ListRepositories(ctx)

	assert.NoError(t, err)
	assert.Equal(t, []string{"myapp", "otherapp", "thirdapp"}, repos)
}

func TestService_ListRepositories_Error(t *testing.T) {
	blobStorage := mocks.NewMockBlobStorage(t)
	manifestStorage := mocks.NewMockManifestStorage(t)
	eventBus := mocks.NewMockEventPublisher(t)

	svc := NewService(blobStorage, manifestStorage, eventBus)
	ctx := testContext()

	manifestStorage.EXPECT().ListRepositories().Return(nil, errors.New("storage error"))

	repos, err := svc.ListRepositories(ctx)

	assert.Error(t, err)
	assert.Nil(t, repos)
	assert.Contains(t, err.Error(), "failed to list repositories")
}
