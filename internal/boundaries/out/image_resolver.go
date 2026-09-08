package out

import (
	"context"
)

// ImageResolver resolves an image reference to a content digest without
// pulling or running anything. Installation-registry refs resolve locally;
// external refs honor allowlists and require-digest policy at the adapter.
// Consumed by deployment preflight; the adapter lands at cutover.
type ImageResolver interface {
	// ResolveDigest returns the pinned digest for ref (e.g. sha256:…).
	// Mutable tags are re-resolved on every call; callers pin the result.
	ResolveDigest(ctx context.Context, ref string) (string, error)
}
