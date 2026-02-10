// Package in defines input ports (interfaces) for use cases.
package in

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// ImageService defines image listing and prune operations.
type ImageService interface {
	// ListImages returns runtime images and known registry tags.
	ListImages(ctx context.Context) ([]domain.ImageInfo, error)

	// Prune removes unused images and applies retention.
	Prune(ctx context.Context, keepLast int) (domain.ImagePruneReport, error)
}
