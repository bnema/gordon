package images

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/pkg/runtime"
)

type localRuntimeImageManager struct {
	runtime imageRuntime
}

// NewLocalRuntimeImageManager creates a monolith/local implementation of the narrow runtime image port.
func NewLocalRuntimeImageManager(runtime imageRuntime) out.RuntimeImageManager {
	return localRuntimeImageManager{runtime: runtime}
}

func (m localRuntimeImageManager) ListRuntimeImages(ctx context.Context) ([]domain.RuntimeImageDetail, error) {
	if m.runtime == nil {
		return nil, fmt.Errorf("runtime image manager not configured")
	}
	details, err := m.runtime.ListImagesDetailed(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]domain.RuntimeImageDetail, 0, len(details))
	for _, detail := range details {
		out = append(out, runtimeImageDetail(detail))
	}
	return out, nil
}

func (m localRuntimeImageManager) PruneRuntimeImages(ctx context.Context, danglingOnly bool) (domain.RuntimePruneResult, error) {
	if m.runtime == nil {
		return domain.RuntimePruneResult{}, fmt.Errorf("runtime image manager not configured")
	}
	report, err := m.runtime.PruneImages(ctx, danglingOnly)
	if err != nil {
		return domain.RuntimePruneResult{}, err
	}
	return domain.RuntimePruneResult{DeletedCount: len(report.DeletedIDs), SpaceReclaimed: report.SpaceReclaimed}, nil
}

func runtimeImageDetail(detail runtime.ImageDetail) domain.RuntimeImageDetail {
	return domain.RuntimeImageDetail{ID: detail.ID, RepoTags: append([]string(nil), detail.RepoTags...), Size: detail.Size, Created: detail.Created}
}
