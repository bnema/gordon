package registry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestStandaloneRegistryPublishesTypedImagePushedEvent(t *testing.T) {
	blobs := mocks.NewMockBlobStorage(t)
	manifests := mocks.NewMockManifestStorage(t)
	events := mocks.NewMockComponentEventPublisher(t)
	data := []byte(`{"schemaVersion":2}`)
	manifests.EXPECT().PutManifest("library/app", "v1", "application/vnd.oci.image.manifest.v1+json", data).Return(nil)
	events.EXPECT().PublishComponentEvent(mock.Anything, mock.MatchedBy(func(event domain.ComponentEventEnvelope) bool {
		payload, ok := event.Payload.(domain.RegistryImagePushedPayload)
		return ok && event.Type == domain.ComponentEventTypeRegistryImagePushed && event.Origin == domain.ComponentRoleRegistry && payload.Repository == "library/app" && payload.Reference == "v1" && payload.Digest == "sha256:bafebd36189ad3688b7b3915ea55d461e0bfcfbdde11e54b0a123999fb6be50f" && string(payload.Manifest) == string(data) && payload.Annotations["org.opencontainers.image.source"] == "https://example.test/app" && event.IdempotencyKey == registryImagePushedIdempotencyKey("library/app", payload.Digest)
	})).Return(nil)
	svc := NewServiceWithComponentEvents(blobs, manifests, nil, events)
	_, err := svc.PutManifest(context.Background(), &domain.Manifest{Name: "library/app", Reference: "v1", ContentType: "application/vnd.oci.image.manifest.v1+json", Data: data, Annotations: map[string]string{"org.opencontainers.image.source": "https://example.test/app"}})
	require.NoError(t, err)
}
