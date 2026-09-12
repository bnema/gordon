// Package images implements the image management use case.
package images

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/registrystate"
	"github.com/bnema/gordon/pkg/runtime"
	"github.com/bnema/gordon/pkg/validation"
)

// Service implements image list and prune operations.
type Service struct {
	runtime         imageRuntime
	manifestStorage out.ManifestStorage
	blobStorage     out.BlobStorage
	log             zerowrap.Logger
	mutationMu      *sync.RWMutex
	registryState   *registrystate.State
	// protection, pruneRuntime, and barrier are the prune ports. All
	// three are required for Prune; a missing port fails closed.
	protection   out.PruneProtectionStore
	pruneRuntime out.PruneRuntime
	barrier      out.GCBarrier
}

type imageRuntime interface {
	ListImagesDetailed(ctx context.Context) ([]runtime.ImageDetail, error)
}

// NewService creates a new images service.
func NewService(
	rt imageRuntime,
	manifestStorage out.ManifestStorage,
	blobStorage out.BlobStorage,
	log zerowrap.Logger,
	states ...*registrystate.State,
) *Service {
	registryState := registrystate.New()
	if len(states) > 0 && states[0] != nil {
		registryState = states[0]
	}
	return &Service{
		runtime:         rt,
		manifestStorage: manifestStorage,
		blobStorage:     blobStorage,
		log:             log,
		mutationMu:      &registryState.MutationMu,
		registryState:   registryState,
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
