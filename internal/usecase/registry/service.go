// Package registry implements the container registry use case.
package registry

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"errors"
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
	blobStorage      out.BlobStorage
	manifestStorage  out.ManifestStorage
	eventBus         out.EventPublisher
	metrics          *telemetry.Metrics
	suppressedImages sync.Map // imageName -> *time.Timer
	mutationMu       *sync.RWMutex
	registryState    *registrystate.State
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

// SuppressDeployEvent marks an image name to skip image.pushed events.
// The suppression auto-expires after 2 minutes to prevent leaks.
func (s *Service) SuppressDeployEvent(imageName string) {
	imageName = ExtractImageName(strings.TrimSpace(imageName))
	if imageName == "" {
		return
	}

	var timer *time.Timer
	timer = time.AfterFunc(2*time.Minute, func() {
		// Only delete if this timer is still the current one, preventing an
		// old timer's callback from removing a newer suppression entry.
		if v, ok := s.suppressedImages.Load(imageName); ok && v == timer {
			s.suppressedImages.Delete(imageName)
		}
	})
	if existing, loaded := s.suppressedImages.LoadOrStore(imageName, timer); loaded {
		existing.(*time.Timer).Stop()
		s.suppressedImages.Store(imageName, timer)
	}
}

// ClearDeployEventSuppression removes event suppression for an image.
func (s *Service) ClearDeployEventSuppression(imageName string) {
	imageName = ExtractImageName(strings.TrimSpace(imageName))
	if imageName == "" {
		return
	}

	if v, loaded := s.suppressedImages.LoadAndDelete(imageName); loaded {
		v.(*time.Timer).Stop()
	}
}

// ExtractImageName returns just the repository path of a container image
// reference, stripping any registry host prefix, tag, and digest.
// Examples:
//
//	"reg.example.com/team/my-app:latest" -> "team/my-app"
//	"reg.example.com/my-app@sha256:abc"  -> "my-app"
//	"my-app:v1.2"                        -> "my-app"
func ExtractImageName(imageRef string) string {
	name := imageRef
	// Strip digest
	if idx := strings.Index(name, "@"); idx != -1 {
		name = name[:idx]
	}
	// Strip tag
	// Find the last colon, but only strip it if it comes after any slash
	// (to avoid treating a port number in the host as a tag).
	if idx := strings.LastIndex(name, ":"); idx != -1 {
		slashIdx := strings.LastIndex(name, "/")
		if idx > slashIdx {
			name = name[:idx]
		}
	}
	// Strip registry host: if the first segment contains a dot or colon it is
	// a registry hostname; remove it.
	parts := strings.SplitN(name, "/", 2)
	if len(parts) == 2 {
		host := parts[0]
		if strings.ContainsAny(host, ".:") || host == "localhost" {
			name = parts[1]
		}
	}
	return name
}

// IsDeployEventSuppressed checks if deploy events are suppressed for an image.
func (s *Service) IsDeployEventSuppressed(imageName string) bool {
	imageName = ExtractImageName(strings.TrimSpace(imageName))
	if imageName == "" {
		return false
	}

	_, exists := s.suppressedImages.Load(imageName)
	return exists
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
		if s.IsDeployEventSuppressed(manifest.Name) {
			log.Info().Str("image", manifest.Name).Msg("skipping image.pushed event: CLI deploy intent active")
		} else {
			if err := s.eventBus.Publish(domain.EventImagePushed, domain.ImagePushedPayload{
				Name:        manifest.Name,
				Reference:   manifest.Reference,
				Manifest:    manifest.Data,
				Annotations: manifest.Annotations,
			}); err != nil {
				log.Warn().Err(err).Msg("failed to publish image pushed event")
			}
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

// GetBlobPath returns the filesystem path to a blob only when a manifest in
// the requested repository references it.
func (s *Service) GetBlobPath(ctx context.Context, name, digest string) (string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetBlobPath",
		"name":                name,
		"digest":              digest,
	})
	log := zerowrap.FromCtx(ctx)

	referenced, err := s.repositoryReferencesDigest(name, digest)
	if err != nil {
		return "", log.WrapErr(err, "failed to verify blob ownership")
	}
	if !referenced {
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

func (s *Service) repositoryReferencesDigest(name, target string) (bool, error) {
	tags, err := s.manifestStorage.ListTags(name)
	if err != nil {
		if errors.Is(err, domain.ErrManifestNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("list tags for repository %s: %w", name, err)
	}

	if len(tags) > maxManifestTraversal {
		return false, fmt.Errorf("%w: repository %s exceeds manifest traversal limit", domain.ErrBlobNotFound, name)
	}
	queue := append([]string(nil), tags...)
	seen := make(map[string]struct{}, len(queue))
	for len(queue) > 0 {
		reference := queue[0]
		queue = queue[1:]
		if _, ok := seen[reference]; ok {
			continue
		}
		seen[reference] = struct{}{}
		if len(seen) > maxManifestTraversal {
			return false, fmt.Errorf("%w: repository %s exceeds manifest traversal limit", domain.ErrBlobNotFound, name)
		}

		data, _, err := s.manifestStorage.GetManifest(name, reference)
		if err != nil {
			return false, fmt.Errorf("get manifest %s for repository %s: %w", reference, name, err)
		}
		var refs manifestReferences
		if err := json.Unmarshal(data, &refs); err != nil {
			return false, fmt.Errorf("decode manifest %s: %w", reference, err)
		}

		if manifestReferencesTarget(refs, target) {
			return true, nil
		}
		nestedManifests := refs.Manifests
		if refs.Subject != nil && refs.Subject.Digest != "" {
			nestedManifests = append(append([]manifestDescriptor(nil), refs.Manifests...), *refs.Subject)
		}
		if len(seen)+len(queue)+len(nestedManifests) > maxManifestTraversal {
			return false, fmt.Errorf("%w: repository %s exceeds manifest traversal limit", domain.ErrBlobNotFound, name)
		}
		queue = appendManifestDigests(queue, nestedManifests)
	}
	return false, nil
}

func manifestReferencesTarget(refs manifestReferences, target string) bool {
	return refs.Config.Digest == target ||
		descriptorListContains(refs.Layers, target) ||
		descriptorListContains(refs.Manifests, target) ||
		descriptorListContains(refs.Blobs, target) ||
		(refs.Subject != nil && refs.Subject.Digest == target)
}

func appendManifestDigests(queue []string, descriptors []manifestDescriptor) []string {
	for _, descriptor := range descriptors {
		if descriptor.Digest != "" {
			queue = append(queue, descriptor.Digest)
		}
	}
	return queue
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

func descriptorListContains(descriptors []manifestDescriptor, digest string) bool {
	for _, descriptor := range descriptors {
		if descriptor.Digest == digest {
			return true
		}
	}
	return false
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

// FinishUpload completes a blob upload.
func (s *Service) FinishUpload(ctx context.Context, uuid, digest string) error {
	// Keep the transition from upload to blob storage atomic with respect to
	// registry garbage collection. PruneRegistry holds the exclusive lock, so
	// it cannot observe a finalized blob before it is marked pending.
	s.mutationMu.RLock()
	defer s.mutationMu.RUnlock()

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
	s.registryState.AddPending(digest, time.Now().UTC())

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
