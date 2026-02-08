// Package images implements the image management use case.
package images

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/runtime"
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

// ListImages returns images known by the runtime.
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

	images := make([]domain.ImageInfo, 0, len(details))
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
		tags, err := s.manifestStorage.ListTags(repository)
		if err != nil {
			return domain.ImagePruneReport{}, log.WrapErr(err, "failed to list repository tags")
		}

		if len(tags) == 0 {
			continue
		}

		tagInfos := make([]registryTag, 0, len(tags))
		for _, tag := range tags {
			modTime, err := s.manifestStorage.GetManifestModTime(repository, tag)
			if err != nil {
				return domain.ImagePruneReport{}, log.WrapErr(err, "failed to read manifest modification time")
			}
			tagInfos = append(tagInfos, registryTag{name: tag, modTime: modTime})
		}

		sort.Slice(tagInfos, func(i, j int) bool {
			if tagInfos[i].modTime.Equal(tagInfos[j].modTime) {
				return tagInfos[i].name > tagInfos[j].name
			}
			return tagInfos[i].modTime.After(tagInfos[j].modTime)
		})

		keptTags := make(map[string]struct{})
		if containsTag(tagInfos, "latest") {
			keptTags["latest"] = struct{}{}
		}
		for i := 0; i < len(tagInfos) && i < keepLast; i++ {
			keptTags[tagInfos[i].name] = struct{}{}
		}

		for _, tag := range tagInfos {
			if _, kept := keptTags[tag.name]; kept {
				continue
			}
			if err := s.manifestStorage.DeleteManifest(repository, tag.name); err != nil {
				return domain.ImagePruneReport{}, log.WrapErr(err, "failed to delete manifest")
			}
			report.Registry.TagsRemoved++
		}

		for _, tag := range tagInfos {
			if _, kept := keptTags[tag.name]; !kept {
				continue
			}

			if err := s.collectReferencedDigests(log, repository, tag.name, referencedDigests, make(map[string]struct{})); err != nil {
				return domain.ImagePruneReport{}, err
			}
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

// Prune runs runtime prune and registry prune and aggregates reports.
func (s *Service) Prune(ctx context.Context, keepLast int) (domain.ImagePruneReport, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Prune",
		"keepLast":            keepLast,
	})
	log := zerowrap.FromCtx(ctx)

	var report domain.ImagePruneReport

	runtimeReport, err := s.PruneRuntime(ctx)
	if err != nil {
		log.Warn().Err(err).Msg("runtime prune failed; continuing with registry prune")
	} else {
		report.Runtime = runtimeReport.Runtime
	}

	registryReport, err := s.PruneRegistry(ctx, keepLast)
	if err != nil {
		return report, err
	}

	report.Registry = registryReport.Registry
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
		return log.WrapErr(err, "failed to read manifest")
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

	idx := strings.LastIndex(repoTag, ":")
	if idx <= 0 || idx >= len(repoTag)-1 {
		return repoTag, ""
	}

	return repoTag[:idx], repoTag[idx+1:]
}
