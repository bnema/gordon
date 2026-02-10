// Package registry implements the container registry use case.
package registry

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	"github.com/bnema/zerowrap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/validation"
)

var registryTracer = otel.Tracer("gordon.registry")

// Service implements the RegistryService interface.
type Service struct {
	blobStorage     out.BlobStorage
	manifestStorage out.ManifestStorage
	eventBus        out.EventPublisher
	metrics         *telemetry.Metrics
}

// SetMetrics sets the telemetry metrics for the registry service.
func (s *Service) SetMetrics(m *telemetry.Metrics) {
	s.metrics = m
}

// NewService creates a new registry service.
func NewService(
	blobStorage out.BlobStorage,
	manifestStorage out.ManifestStorage,
	eventBus out.EventPublisher,
) *Service {
	return &Service{
		blobStorage:     blobStorage,
		manifestStorage: manifestStorage,
		eventBus:        eventBus,
	}
}

// GetManifest retrieves a manifest by name and reference.
func (s *Service) GetManifest(ctx context.Context, name, reference string) (*domain.Manifest, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetManifest",
		"name":                name,
		"reference":           reference,
	})
	log := zerowrap.FromCtx(ctx)

	data, contentType, err := s.manifestStorage.GetManifest(name, reference)
	if err != nil {
		return nil, log.WrapErr(err, "failed to get manifest")
	}

	return &domain.Manifest{
		Name:        name,
		Reference:   reference,
		ContentType: contentType,
		Data:        data,
	}, nil
}

// PutManifest stores a manifest and returns the calculated digest.
func (s *Service) PutManifest(ctx context.Context, manifest *domain.Manifest) (string, error) {
	ctx, span := registryTracer.Start(ctx, "registry.put_manifest",
		trace.WithAttributes(
			attribute.String("name", manifest.Name),
			attribute.String("reference", manifest.Reference),
			attribute.Int("manifest_size", len(manifest.Data)),
		))
	defer span.End()

	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "PutManifest",
		"name":                manifest.Name,
		"reference":           manifest.Reference,
	})
	log := zerowrap.FromCtx(ctx)

	// Calculate digest
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest.Data))

	if err := s.manifestStorage.PutManifest(manifest.Name, manifest.Reference, manifest.ContentType, manifest.Data); err != nil {
		return "", log.WrapErr(err, "failed to store manifest")
	}

	// Record push metrics
	if s.metrics != nil {
		attrs := metric.WithAttributes(
			attribute.String("name", manifest.Name),
			attribute.String("reference", manifest.Reference),
		)
		s.metrics.ImagePushTotal.Add(ctx, 1, attrs)
		s.metrics.ImagePushSize.Add(ctx, int64(len(manifest.Data)), attrs)
	}

	// Publish image pushed event only for tag references (not digests).
	// A docker push sends manifests by both digest and tag; firing only on
	// tag prevents duplicate deploy triggers for the same push.
	if s.eventBus != nil && !strings.HasPrefix(manifest.Reference, "sha256:") {
		if err := s.eventBus.Publish(domain.EventImagePushed, domain.ImagePushedPayload{
			Name:        manifest.Name,
			Reference:   manifest.Reference,
			Manifest:    manifest.Data,
			Annotations: manifest.Annotations,
		}); err != nil {
			log.Warn().Err(err).Msg("failed to publish image pushed event")
		}
	}

	log.Info().Str("digest", digest).Msg("manifest stored")
	return digest, nil
}

// DeleteManifest removes a manifest.
func (s *Service) DeleteManifest(ctx context.Context, name, reference string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "DeleteManifest",
		"name":                name,
		"reference":           reference,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.manifestStorage.DeleteManifest(name, reference); err != nil {
		return log.WrapErr(err, "failed to delete manifest")
	}

	log.Info().Msg("manifest deleted")
	return nil
}

// GetBlob retrieves a blob by digest.
func (s *Service) GetBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetBlob",
		"digest":              digest,
	})
	log := zerowrap.FromCtx(ctx)

	reader, err := s.blobStorage.GetBlob(digest)
	if err != nil {
		return nil, log.WrapErr(err, "failed to get blob")
	}

	return reader, nil
}

// GetBlobPath returns the filesystem path to a blob for direct serving.
func (s *Service) GetBlobPath(ctx context.Context, digest string) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetBlobPath",
		"digest":              digest,
	})
	log := zerowrap.FromCtx(ctx)

	path, err := s.blobStorage.GetBlobPath(digest)
	if err != nil {
		return "", log.WrapErr(err, "failed to get blob path")
	}

	return path, nil
}

// PutBlob stores a blob with the given digest.
func (s *Service) PutBlob(ctx context.Context, digest string, data io.Reader, size int64) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "PutBlob",
		"digest":              digest,
		zerowrap.FieldSize:    size,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.blobStorage.PutBlob(digest, data, size); err != nil {
		return log.WrapErr(err, "failed to store blob")
	}

	log.Info().Msg("blob stored")
	return nil
}

// BlobExists checks if a blob exists.
func (s *Service) BlobExists(_ context.Context, digest string) bool {
	return s.blobStorage.BlobExists(digest)
}

// StartUpload starts a new blob upload.
func (s *Service) StartUpload(ctx context.Context, name string) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "StartUpload",
		"name":                name,
	})
	log := zerowrap.FromCtx(ctx)

	uuid, err := s.blobStorage.StartBlobUpload(name)
	if err != nil {
		return "", log.WrapErr(err, "failed to start blob upload")
	}

	log.Info().Str("uuid", uuid).Msg("blob upload started")
	return uuid, nil
}

// AppendBlobChunk appends data to an in-progress blob upload.
func (s *Service) AppendBlobChunk(ctx context.Context, name, uuid string, data io.Reader) (int64, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "AppendBlobChunk",
		"name":                name,
		"uuid":                uuid,
	})
	log := zerowrap.FromCtx(ctx)

	length, err := s.blobStorage.AppendBlobChunk(name, uuid, data)
	if err != nil {
		return 0, log.WrapErr(err, "failed to append blob chunk")
	}

	return length, nil
}

// FinishUpload completes a blob upload.
func (s *Service) FinishUpload(ctx context.Context, uuid, digest string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "FinishUpload",
		"uuid":                uuid,
		"digest":              digest,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.blobStorage.FinishBlobUpload(uuid, digest); err != nil {
		return log.WrapErr(err, "failed to finish blob upload")
	}

	log.Info().Msg("blob upload finished")
	return nil
}

// CancelUpload cancels an in-progress upload.
func (s *Service) CancelUpload(ctx context.Context, uuid string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "CancelUpload",
		"uuid":                uuid,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.blobStorage.CancelBlobUpload(uuid); err != nil {
		return log.WrapErr(err, "failed to cancel blob upload")
	}

	log.Info().Msg("blob upload cancelled")
	return nil
}

// ListTags returns all tags for a repository.
func (s *Service) ListTags(ctx context.Context, name string) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListTags",
		"name":                name,
	})
	log := zerowrap.FromCtx(ctx)

	tags, err := s.manifestStorage.ListTags(name)
	if err != nil {
		return nil, log.WrapErr(err, "failed to list tags")
	}

	filtered := make([]string, 0, len(tags))
	for _, tag := range tags {
		if validation.IsDigest(tag) {
			continue
		}
		filtered = append(filtered, tag)
	}

	return filtered, nil
}

// ListRepositories returns all repository names.
func (s *Service) ListRepositories(ctx context.Context) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListRepositories",
	})
	log := zerowrap.FromCtx(ctx)

	repos, err := s.manifestStorage.ListRepositories()
	if err != nil {
		return nil, log.WrapErr(err, "failed to list repositories")
	}

	return repos, nil
}
