package registry

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

const registryImagePushedAction = "image_pushed"

// registryImagePushedIdempotencyKey is intentionally derived only from the
// image identity, immutable manifest digest, and action. It contains no
// manifest content, annotation, credential, or user supplied secret material.
func registryImagePushedIdempotencyKey(image, digest string) string {
	return fmt.Sprintf("%s:%s:%s", image, digest, registryImagePushedAction)
}

func (s *Service) publishComponentImagePushed(ctx context.Context, image, reference, digest string) error {
	key := registryImagePushedIdempotencyKey(image, digest)
	id := fmt.Sprintf("registry-%x", sha256.Sum256([]byte(key)))
	return s.componentEvents.PublishComponentEvent(ctx, domain.ComponentEventEnvelope{
		ID:                  id,
		Type:                domain.ComponentEventTypeRegistryImagePushed,
		Origin:              domain.ComponentRoleRegistry,
		Timestamp:           time.Now().UTC(),
		IdempotencyKey:      key,
		AuditClassification: domain.ComponentEventAuditCritical,
		Payload: domain.RegistryImagePushedPayload{
			Repository: image,
			Reference:  reference,
			Digest:     digest,
		},
	})
}
