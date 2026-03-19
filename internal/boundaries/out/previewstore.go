package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// PreviewStore persists active preview routes.
type PreviewStore interface {
	Load(ctx context.Context) ([]domain.PreviewRoute, error)
	Save(ctx context.Context, previews []domain.PreviewRoute) error
}
