// Package images implements the image management use case.
package images

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/runtime"
	"github.com/bnema/gordon/pkg/validation"
)

// Service implements image list and prune operations.
type Service struct {
	runtime         imageRuntime
	manifestStorage out.ManifestStorage
	blobStorage     out.BlobStorage
	log             zerowrap.Logger
}

type imageRuntime interface {
	ListImagesDetailed(ctx context.Context) ([]runtime.ImageDetail, error)
	PruneImages(ctx context.Context, danglingOnly bool) (runtime.PruneReport, error)
}

// NewService creates a new images service.
func NewService(
	rt imageRuntime,
	manifestStorage out.ManifestStorage,
	blobStorage out.BlobStorage,
	log zerowrap.Logger,
) *Service {
	return &Service{
		runtime:         rt,
		manifestStorage: manifestStorage,
		blobStorage:     blobStorage,
		log:             log,
	}
}

// ListImages returns images known by the runtime and registry tags.
func (s *Service) ListImages(ctx context.Context) ([]domain.ImageInfo, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListImages",
	})
	log := zerowrap.FromCtx(ctx)

	details, err := s.runtime.ListImagesDetailed(ctx)
	if err != nil {
		return nil, log.WrapErr(err, "failed to list images")
	}

	images, seenRepoTags, repoDisplayByNormalized := buildRuntimeImageIndex(details)
	images, err = s.appendRegistryImages(log, images, seenRepoTags, repoDisplayByNormalized)
	if err != nil {
		return nil, err
	}

	sort.SliceStable(images, func(i, j int) bool {
		return lessImageInfo(images[i], images[j])
	})

	return images, nil
}

func buildRuntimeImageIndex(details []runtime.ImageDetail) ([]domain.ImageInfo, map[string]struct{}, map[string]string) {
	images := make([]domain.ImageInfo, 0, len(details))
	seenRepoTags := make(map[string]struct{}, len(details))
	repoDisplayByNormalized := make(map[string]string)

	for _, detail := range details {
		if isDanglingImage(detail.RepoTags) {
			images = append(images, domain.ImageInfo{
				Repository: "",
				Tag:        "",
				Size:       detail.Size,
				Created:    detail.Created,
				ID:         detail.ID,
				Dangling:   true,
			})
			continue
		}

		for _, repoTag := range detail.RepoTags {
			if repoTag == "" || repoTag == "<none>:<none>" {
				continue
			}
			repository, tag := splitRepoTag(repoTag)
			normalizedRepository := normalizeRepository(repository)
			seenRepoTags[repoTagKey(normalizedRepository, tag)] = struct{}{}
			if normalizedRepository != "" && normalizedRepository != repository {
				repoDisplayByNormalized[normalizedRepository] = repository
			}
			images = append(images, domain.ImageInfo{
				Repository: repository,
				Tag:        tag,
				Size:       detail.Size,
				Created:    detail.Created,
				ID:         detail.ID,
				Dangling:   false,
			})
		}
	}

	return images, seenRepoTags, repoDisplayByNormalized
}

func (s *Service) appendRegistryImages(
	log zerowrap.Logger,
	images []domain.ImageInfo,
	seenRepoTags map[string]struct{},
	repoDisplayByNormalized map[string]string,
) ([]domain.ImageInfo, error) {
	repositories, err := s.manifestStorage.ListRepositories()
	if err != nil {
		return nil, log.WrapErr(err, "failed to list repositories")
	}

	sort.Strings(repositories)
	for _, repository := range repositories {
		tags, err := s.manifestStorage.ListTags(repository)
		if err != nil {
			return nil, log.WrapErr(err, "failed to list repository tags")
		}

		displayRepository := repository
		normalizedRepository := normalizeRepository(repository)
		if mappedRepository, ok := repoDisplayByNormalized[normalizedRepository]; ok {
			displayRepository = mappedRepository
		}

		for _, tag := range tags {
			if isRegistryTagPlaceholder(tag) {
				continue
			}

			key := repoTagKey(normalizedRepository, tag)
			if _, exists := seenRepoTags[key]; exists {
				continue
			}

			createdAt, err := s.manifestStorage.GetManifestModTime(repository, tag)
			if err != nil {
				log.Warn().
					Err(err).
					Str("repository", repository).
					Str("tag", tag).
					Msg("failed to read manifest modification time")
			}

			images = append(images, domain.ImageInfo{
				Repository: displayRepository,
				Tag:        tag,
				Created:    createdAt,
				Dangling:   false,
			})
			seenRepoTags[key] = struct{}{}
		}
	}

	return images, nil
}

// PruneRuntime prunes dangling images from the runtime.
func (s *Service) PruneRuntime(ctx context.Context) (domain.ImagePruneReport, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "PruneRuntime",
	})
	log := zerowrap.FromCtx(ctx)

	pruneReport, err := s.runtime.PruneImages(ctx, true)
	if err != nil {
		return domain.ImagePruneReport{}, log.WrapErr(err, "failed to prune runtime images")
	}

	return domain.ImagePruneReport{
		Runtime: domain.RuntimePruneResult{
			DeletedCount:   len(pruneReport.DeletedIDs),
			SpaceReclaimed: pruneReport.SpaceReclaimed,
		},
	}, nil
}

// PruneRegistry applies tag retention and blob garbage collection.
// It keeps the "latest" tag when present and keeps keepLast most-recent
// non-latest tags from tagInfos.
func (s *Service) PruneRegistry(ctx context.Context, keepLast int) (domain.ImagePruneReport, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "PruneRegistry",
		"keepLast":            keepLast,
	})
	log := zerowrap.FromCtx(ctx)

	if keepLast <= 0 {
		return domain.ImagePruneReport{}, nil
	}

	repositories, err := s.manifestStorage.ListRepositories()
	if err != nil {
		return domain.ImagePruneReport{}, log.WrapErr(err, "failed to list repositories")
	}

	referencedDigests := make(map[string]struct{})
	report := domain.ImagePruneReport{}

	for _, repository := range repositories {
		tagInfos, err := s.loadRepositoryTagInfos(repository)
		if err != nil {
			return domain.ImagePruneReport{}, log.WrapErr(err, "failed to load repository tags")
		}
		if len(tagInfos) == 0 {
			continue
		}

		keptTags := buildKeptTagSet(tagInfos, keepLast)
		removed, err := s.deleteUnkeptManifests(repository, tagInfos, keptTags)
		if err != nil {
			return domain.ImagePruneReport{}, log.WrapErr(err, "failed to delete manifest")
		}
		report.Registry.TagsRemoved += removed

		if err := s.collectKeptTagDigests(log, repository, tagInfos, keptTags, referencedDigests); err != nil {
			return domain.ImagePruneReport{}, err
		}
	}

	blobs, err := s.blobStorage.ListBlobs()
	if err != nil {
		return domain.ImagePruneReport{}, log.WrapErr(err, "failed to list blobs")
	}

	for _, digest := range blobs {
		if _, referenced := referencedDigests[digest]; referenced {
			continue
		}
		if err := s.blobStorage.DeleteBlob(digest); err != nil {
			return domain.ImagePruneReport{}, log.WrapErr(err, "failed to delete blob")
		}
		report.Registry.BlobsRemoved++
	}

	return report, nil
}

func (s *Service) loadRepositoryTagInfos(repository string) ([]registryTag, error) {
	tags, err := s.manifestStorage.ListTags(repository)
	if err != nil {
		return nil, err
	}

	tagInfos := make([]registryTag, 0, len(tags))
	for _, tag := range tags {
		modTime, err := s.manifestStorage.GetManifestModTime(repository, tag)
		if err != nil {
			return nil, err
		}
		tagInfos = append(tagInfos, registryTag{name: tag, modTime: modTime})
	}

	sort.Slice(tagInfos, func(i, j int) bool {
		if tagInfos[i].modTime.Equal(tagInfos[j].modTime) {
			return tagInfos[i].name > tagInfos[j].name
		}
		return tagInfos[i].modTime.After(tagInfos[j].modTime)
	})

	return tagInfos, nil
}

func buildKeptTagSet(tagInfos []registryTag, keepLast int) map[string]struct{} {
	keptTags := make(map[string]struct{})
	if containsTag(tagInfos, "latest") {
		keptTags["latest"] = struct{}{}
	}

	kept := 0
	for _, tagInfo := range tagInfos {
		if tagInfo.name == "latest" {
			continue
		}
		if kept >= keepLast {
			break
		}
		keptTags[tagInfo.name] = struct{}{}
		kept++
	}

	return keptTags
}

func (s *Service) deleteUnkeptManifests(repository string, tagInfos []registryTag, keptTags map[string]struct{}) (int, error) {
	removed := 0
	for _, tag := range tagInfos {
		if _, kept := keptTags[tag.name]; kept {
			continue
		}
		if err := s.manifestStorage.DeleteManifest(repository, tag.name); err != nil {
			return 0, err
		}
		removed++
	}

	return removed, nil
}

func (s *Service) collectKeptTagDigests(
	log zerowrap.Logger,
	repository string,
	tagInfos []registryTag,
	keptTags map[string]struct{},
	referencedDigests map[string]struct{},
) error {
	for _, tag := range tagInfos {
		if _, kept := keptTags[tag.name]; !kept {
			continue
		}
		if err := s.collectReferencedDigests(log, repository, tag.name, referencedDigests, make(map[string]struct{})); err != nil {
			return err
		}
	}

	return nil
}

// Prune runs runtime and/or registry prune based on options and aggregates reports.
func (s *Service) Prune(ctx context.Context, opts domain.ImagePruneOptions) (domain.ImagePruneReport, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Prune",
		"keepLast":            opts.KeepLast,
		"pruneDangling":       opts.PruneDangling,
		"pruneRegistry":       opts.PruneRegistry,
	})
	log := zerowrap.FromCtx(ctx)

	var report domain.ImagePruneReport

	if opts.PruneDangling {
		runtimeReport, err := s.PruneRuntime(ctx)
		if err != nil {
			if !opts.PruneRegistry {
				// Dangling-only: surface the error to the caller.
				return report, fmt.Errorf("runtime prune failed: %w", err)
			}
			log.Warn().Err(err).Msg("runtime prune failed; continuing with registry prune")
		} else {
			report.Runtime = runtimeReport.Runtime
		}
	}

	if opts.PruneRegistry {
		registryReport, err := s.PruneRegistry(ctx, opts.KeepLast)
		if err != nil {
			return report, err
		}
		report.Registry = registryReport.Registry
	}

	return report, nil
}

type registryTag struct {
	name    string
	modTime time.Time
}

type manifestReferences struct {
	blobs          []string
	childManifests []string
}

func containsTag(tags []registryTag, name string) bool {
	for _, tag := range tags {
		if tag.name == name {
			return true
		}
	}

	return false
}

func (s *Service) collectReferencedDigests(
	log zerowrap.Logger,
	repository, reference string,
	referencedDigests map[string]struct{},
	visited map[string]struct{},
) error {
	visitKey := repository + "@" + reference
	if _, seen := visited[visitKey]; seen {
		return nil
	}
	visited[visitKey] = struct{}{}

	manifestData, _, err := s.manifestStorage.GetManifest(repository, reference)
	if err != nil {
		log.Warn().
			Str("repository", repository).
			Str("reference", reference).
			Err(err).
			Msg("manifest not found during prune; skipping (orphaned reference)")
		return nil
	}

	refs, err := parseManifestReferences(manifestData)
	if err != nil {
		return log.WrapErr(err, "failed to parse manifest")
	}

	for _, digest := range refs.blobs {
		referencedDigests[digest] = struct{}{}
	}

	for _, childRef := range refs.childManifests {
		referencedDigests[childRef] = struct{}{}
		if err := s.collectReferencedDigests(log, repository, childRef, referencedDigests, visited); err != nil {
			return err
		}
	}

	return nil
}

func parseManifestReferences(data []byte) (manifestReferences, error) {
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		Layers []struct {
			Digest string `json:"digest"`
		} `json:"layers"`
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}

	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifestReferences{}, err
	}

	refs := manifestReferences{
		blobs:          make([]string, 0, len(manifest.Layers)+1),
		childManifests: make([]string, 0, len(manifest.Manifests)),
	}

	if manifest.Config.Digest != "" {
		refs.blobs = append(refs.blobs, manifest.Config.Digest)
	}

	for _, layer := range manifest.Layers {
		if layer.Digest == "" {
			continue
		}
		refs.blobs = append(refs.blobs, layer.Digest)
	}

	for _, child := range manifest.Manifests {
		if child.Digest == "" {
			continue
		}
		refs.childManifests = append(refs.childManifests, child.Digest)
	}

	return refs, nil
}

func isDanglingImage(repoTags []string) bool {
	if len(repoTags) == 0 {
		return true
	}

	for _, tag := range repoTags {
		if tag == "<none>:<none>" || tag == "" {
			continue
		}
		return false
	}

	return true
}

func splitRepoTag(repoTag string) (string, string) {
	if repoTag == "" {
		return "", ""
	}
	if strings.Contains(repoTag, "@") {
		return repoTag, ""
	}

	idx := strings.LastIndex(repoTag, ":")
	slashIdx := strings.LastIndex(repoTag, "/")
	if idx <= slashIdx || idx >= len(repoTag)-1 {
		return repoTag, ""
	}

	return repoTag[:idx], repoTag[idx+1:]
}

func normalizeRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return ""
	}

	idx := strings.Index(repository, "/")
	if idx <= 0 {
		return repository
	}

	firstComponent := repository[:idx]
	if strings.Contains(firstComponent, ".") || strings.Contains(firstComponent, ":") || firstComponent == "localhost" {
		return repository[idx+1:]
	}

	return repository
}

func repoTagKey(repository, tag string) string {
	return repository + "\x00" + tag
}

func isRegistryTagPlaceholder(tag string) bool {
	return tag == "" || tag == "<none>" || tag == "<none>:<none>" || validation.IsDigest(tag)
}

func lessImageInfo(left, right domain.ImageInfo) bool {
	if left.Dangling != right.Dangling {
		return !left.Dangling
	}
	if left.Repository != right.Repository {
		return left.Repository < right.Repository
	}
	if left.Tag == "latest" && right.Tag != "latest" {
		return true
	}
	if right.Tag == "latest" && left.Tag != "latest" {
		return false
	}
	if !left.Created.Equal(right.Created) {
		return left.Created.After(right.Created)
	}
	if left.Tag != right.Tag {
		return left.Tag > right.Tag
	}

	return left.ID < right.ID
}
