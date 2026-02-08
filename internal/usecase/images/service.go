// Package images implements the image management use case.
package images

import (
	"context"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/runtime"
)

// Service implements image list and prune operations.
type Service struct {
	runtime         runtime.Runtime
	manifestStorage out.ManifestStorage
	blobStorage     out.BlobStorage
	log             zerowrap.Logger
}

// NewService creates a new images service.
func NewService(
	rt runtime.Runtime,
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
