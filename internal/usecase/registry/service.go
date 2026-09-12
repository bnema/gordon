// Package registry implements the container registry use case.
package registry

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/registrystate"
	"github.com/bnema/gordon/pkg/validation"
)

var registryTracer = otel.Tracer("gordon.registry")

// Service implements the RegistryService interface.
type Service struct {
	blobStorage     out.BlobStorage
	manifestStorage out.ManifestStorage
	eventBus        out.EventPublisher
	metrics         *telemetry.Metrics
	mutationMu      *sync.RWMutex
	registryState   *registrystate.State
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
	states ...*registrystate.State,
) *Service {
	registryState := registrystate.New()
	if len(states) > 0 && states[0] != nil {
		registryState = states[0]
	}
	return &Service{
		blobStorage:     blobStorage,
		manifestStorage: manifestStorage,
		eventBus:        eventBus,
		mutationMu:      &registryState.MutationMu,
		registryState:   registryState,
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
	s.mutationMu.RLock()
	defer s.mutationMu.RUnlock()

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

	// Calculate and verify the digest before mutating storage. A digest-addressed
	// manifest must match its URL reference or clients could later retrieve
	// attacker-controlled bytes under a trusted content address.
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest.Data))
	if validation.IsDigest(manifest.Reference) {
		matches, err := manifestDigestMatches(manifest.Reference, manifest.Data)
		if err != nil {
			return "", fmt.Errorf("validate manifest digest: %w", err)
		}
		if !matches {
			return "", fmt.Errorf("%w: manifest content does not match %s", domain.ErrDigestMismatch, manifest.Reference)
		}
		digest = manifest.Reference
	}

	if err := s.validateManifestBlobs(manifest.Name, manifest.Data); err != nil {
		return "", log.WrapErr(err, "manifest references unowned content")
	}

	if err := s.manifestStorage.PutManifest(manifest.Name, manifest.Reference, manifest.ContentType, manifest.Data); err != nil {
		return "", log.WrapErr(err, "failed to store manifest")
	}
	s.registryState.MarkPublished(manifestReferencedDigests(manifest.Data))

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
	if s.eventBus != nil && !validation.IsDigest(manifest.Reference) {
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
	s.mutationMu.RLock()
	defer s.mutationMu.RUnlock()

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

// GetBlobPath returns the filesystem path to a blob only when the
// repository completed an upload of that digest. Ownership is never
// inferred from manifest references: a repository writer controls its own
// manifests, so a manifest naming a foreign digest must not confer access.
func (s *Service) GetBlobPath(ctx context.Context, name, digest string) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetBlobPath",
		"name":                name,
		"digest":              digest,
	})
	log := zerowrap.FromCtx(ctx)

	owned, err := s.blobStorage.BlobOwnedByRepository(name, digest)
	if err != nil {
		return "", log.WrapErr(err, "failed to verify blob ownership")
	}
	if !owned {
		return "", domain.ErrBlobNotFound
	}

	path, err := s.blobStorage.GetBlobPath(digest)
	if err != nil {
		return "", log.WrapErr(err, "failed to get blob path")
	}

	return path, nil
}

type manifestDescriptor struct {
	Digest string `json:"digest"`
}

type manifestReferences struct {
	Config    manifestDescriptor   `json:"config"`
	Layers    []manifestDescriptor `json:"layers"`
	Manifests []manifestDescriptor `json:"manifests"`
	Blobs     []manifestDescriptor `json:"blobs"`
	Subject   *manifestDescriptor  `json:"subject"`
}

const maxManifestTraversal = 10000

// validateManifestBlobs requires every config/layer blob a manifest
// references to be owned by the repository, and every child manifest to
// already exist in it. A manifest can therefore never manufacture access to
// content the repository did not receive through an authenticated push.
func (s *Service) validateManifestBlobs(name string, data []byte) error {
	var refs manifestReferences
	if err := json.Unmarshal(data, &refs); err != nil {
		return fmt.Errorf("%w: manifest is not a JSON descriptor document", domain.ErrManifestBlobUnknown)
	}

	blobs := make([]manifestDescriptor, 0, 1+len(refs.Layers)+len(refs.Blobs))
	if refs.Config.Digest != "" {
		blobs = append(blobs, refs.Config)
	}
	blobs = append(blobs, refs.Layers...)
	blobs = append(blobs, refs.Blobs...)
	for _, descriptor := range blobs {
		if descriptor.Digest == "" {
			continue
		}
		owned, err := s.blobStorage.BlobOwnedByRepository(name, descriptor.Digest)
		if err != nil {
			return fmt.Errorf("verify blob ownership: %w", err)
		}
		if !owned {
			return fmt.Errorf("%w: %s", domain.ErrManifestBlobUnknown, descriptor.Digest)
		}
	}

	children := refs.Manifests
	if refs.Subject != nil && refs.Subject.Digest != "" {
		children = append(append([]manifestDescriptor(nil), children...), *refs.Subject)
	}
	if len(children) > maxManifestTraversal {
		return fmt.Errorf("%w: manifest references too many children", domain.ErrManifestBlobUnknown)
	}
	for _, descriptor := range children {
		if descriptor.Digest == "" {
			continue
		}
		if _, _, err := s.manifestStorage.GetManifest(name, descriptor.Digest); err != nil {
			return fmt.Errorf("%w: %s", domain.ErrManifestBlobUnknown, descriptor.Digest)
		}
	}
	return nil
}

func manifestReferencedDigests(data []byte) []string {
	var refs manifestReferences
	if json.Unmarshal(data, &refs) != nil {
		return nil
	}
	digests := make([]string, 0, 1+len(refs.Layers)+len(refs.Manifests)+len(refs.Blobs))
	if refs.Config.Digest != "" {
		digests = append(digests, refs.Config.Digest)
	}
	for _, descriptors := range [][]manifestDescriptor{refs.Layers, refs.Manifests, refs.Blobs} {
		for _, descriptor := range descriptors {
			if descriptor.Digest != "" {
				digests = append(digests, descriptor.Digest)
			}
		}
	}
	if refs.Subject != nil && refs.Subject.Digest != "" {
		digests = append(digests, refs.Subject.Digest)
	}
	return digests
}

func manifestDigestMatches(reference string, data []byte) (bool, error) {
	algorithm, _, ok := strings.Cut(reference, ":")
	if !ok {
		return false, domain.ErrInvalidDigest
	}
	switch algorithm {
	case "sha256":
		return reference == fmt.Sprintf("sha256:%x", sha256.Sum256(data)), nil
	case "sha512":
		return reference == fmt.Sprintf("sha512:%x", sha512.Sum512(data)), nil
	default:
		return false, domain.ErrInvalidDigest
	}
}

// PutBlob stores a blob with the given digest.
func (s *Service) PutBlob(ctx context.Context, digest string, data io.Reader, size int64) error {
	s.mutationMu.RLock()
	defer s.mutationMu.RUnlock()

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
	s.registryState.AddPending(digest, time.Now().UTC())

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
func (s *Service) AppendBlobChunk(ctx context.Context, name, uuid string, data io.Reader, contentLength, maxBlobSize int64) (int64, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "AppendBlobChunk",
		"name":                name,
		"uuid":                uuid,
	})
	log := zerowrap.FromCtx(ctx)

	length, err := s.blobStorage.AppendBlobChunk(name, uuid, data, contentLength, maxBlobSize)
	if err != nil {
		return 0, log.WrapErr(err, "failed to append blob chunk")
	}

	return length, nil
}

// FinishUpload completes a blob upload for the named repository and records
// the repository/blob association.
func (s *Service) FinishUpload(ctx context.Context, name, uuid, digest string) error {
	// Keep the transition from upload to blob storage atomic with respect to
	// registry garbage collection. PruneRegistry holds the exclusive lock, so
	// it cannot observe a finalized blob before it is marked pending.
	s.mutationMu.RLock()
	defer s.mutationMu.RUnlock()

	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "FinishUpload",
		"name":                name,
		"uuid":                uuid,
		"digest":              digest,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.blobStorage.FinishBlobUpload(name, uuid, digest); err != nil {
		return log.WrapErr(err, "failed to finish blob upload")
	}
	s.registryState.AddPending(digest, time.Now().UTC())

	log.Info().Msg("blob upload finished")
	return nil
}

// CancelUpload cancels an in-progress upload.
func (s *Service) CancelUpload(ctx context.Context, name, uuid string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "CancelUpload",
		"name":                name,
		"uuid":                uuid,
	})
	log := zerowrap.FromCtx(ctx)

	if err := s.blobStorage.CancelBlobUpload(name, uuid); err != nil {
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
