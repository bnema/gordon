package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestRegistryImagePushedEventContract(t *testing.T) {
	const contentType = "application/vnd.oci.image.manifest.v1+json"
	manifestData := []byte(`{"schemaVersion":2,"annotations":{"org.opencontainers.image.title":"contract"}}`)
	annotations := map[string]string{"org.opencontainers.image.title": "contract"}

	t.Run("tag manifest publish emits image.pushed payload", func(t *testing.T) {
		blobStorage := mocks.NewMockBlobStorage(t)
		manifestStorage := mocks.NewMockManifestStorage(t)
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(blobStorage, manifestStorage, eventBus)
		manifestStorage.EXPECT().PutManifest("library/app", "v1", contentType, manifestData).Return(nil)
		eventBus.EXPECT().Publish(domain.EventImagePushed, mock.MatchedBy(func(payload domain.ImagePushedPayload) bool {
			return payload.Name == "library/app" && payload.Reference == "v1" && string(payload.Manifest) == string(manifestData) && payload.Annotations["org.opencontainers.image.title"] == annotations["org.opencontainers.image.title"]
		})).Return(nil)

		digest, err := svc.PutManifest(testContext(), &domain.Manifest{Name: "library/app", Reference: "v1", ContentType: contentType, Data: manifestData, Annotations: annotations})
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(digest, "sha256:"))
	})

	t.Run("digest manifest publish does not emit duplicate deploy event", func(t *testing.T) {
		blobStorage := mocks.NewMockBlobStorage(t)
		manifestStorage := mocks.NewMockManifestStorage(t)
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(blobStorage, manifestStorage, eventBus)
		manifestStorage.EXPECT().PutManifest("library/app", "sha256:0123456789abcdef", contentType, manifestData).Return(nil)

		_, err := svc.PutManifest(testContext(), &domain.Manifest{Name: "library/app", Reference: "sha256:0123456789abcdef", ContentType: contentType, Data: manifestData})
		require.NoError(t, err)
	})

	t.Run("deploy intent suppression skips event and clear restores current behavior", func(t *testing.T) {
		blobStorage := mocks.NewMockBlobStorage(t)
		manifestStorage := mocks.NewMockManifestStorage(t)
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(blobStorage, manifestStorage, eventBus)
		svc.SuppressDeployEvent("docker.io/library/app:latest")
		require.True(t, svc.IsDeployEventSuppressed("library/app"))
		manifestStorage.EXPECT().PutManifest("library/app", "latest", contentType, manifestData).Return(nil).Once()
		_, err := svc.PutManifest(context.Background(), &domain.Manifest{Name: "library/app", Reference: "latest", ContentType: contentType, Data: manifestData})
		require.NoError(t, err)

		svc.ClearDeployEventSuppression("library/app")
		require.False(t, svc.IsDeployEventSuppressed("library/app"))
		manifestStorage.EXPECT().PutManifest("library/app", "latest", contentType, manifestData).Return(nil).Once()
		eventBus.EXPECT().Publish(domain.EventImagePushed, mock.AnythingOfType("domain.ImagePushedPayload")).Return(nil).Once()
		_, err = svc.PutManifest(context.Background(), &domain.Manifest{Name: "library/app", Reference: "latest", ContentType: contentType, Data: manifestData})
		require.NoError(t, err)
	})

	t.Run("deploy intent suppression expiry restores event publishing", func(t *testing.T) {
		blobStorage := mocks.NewMockBlobStorage(t)
		manifestStorage := mocks.NewMockManifestStorage(t)
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(blobStorage, manifestStorage, eventBus)
		svc.suppressedImages.Store("library/app", time.AfterFunc(time.Hour, func() {}))
		require.True(t, svc.IsDeployEventSuppressed("library/app"))
		svc.suppressedImages.Delete("library/app")
		require.False(t, svc.IsDeployEventSuppressed("library/app"))

		manifestStorage.EXPECT().PutManifest("library/app", "latest", contentType, manifestData).Return(nil).Once()
		eventBus.EXPECT().Publish(domain.EventImagePushed, mock.AnythingOfType("domain.ImagePushedPayload")).Return(nil).Once()
		_, err := svc.PutManifest(context.Background(), &domain.Manifest{Name: "library/app", Reference: "latest", ContentType: contentType, Data: manifestData})
		require.NoError(t, err)
	})

	t.Run("event publish failure logs but does not fail manifest storage", func(t *testing.T) {
		blobStorage := mocks.NewMockBlobStorage(t)
		manifestStorage := mocks.NewMockManifestStorage(t)
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(blobStorage, manifestStorage, eventBus)
		manifestStorage.EXPECT().PutManifest("library/app", "latest", contentType, manifestData).Return(nil)
		eventBus.EXPECT().Publish(domain.EventImagePushed, mock.AnythingOfType("domain.ImagePushedPayload")).Return(errors.New("publish failed"))
		_, err := svc.PutManifest(testContext(), &domain.Manifest{Name: "library/app", Reference: "latest", ContentType: contentType, Data: manifestData})
		require.NoError(t, err)
	})
}
