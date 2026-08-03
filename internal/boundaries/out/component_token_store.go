package out

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// ComponentTokenStore persists component token hashes and safe metadata.
// Implementations must store only TokenHash, never the plaintext token.
type ComponentTokenStore interface {
	// CreateComponentToken stores a newly generated token hash and metadata.
	CreateComponentToken(ctx context.Context, record *domain.ComponentTokenRecord) error

	// LookupComponentToken finds a token record by public token prefix and key ID.
	LookupComponentToken(ctx context.Context, prefix, keyID string) (*domain.ComponentTokenRecord, error)

	// RevokeComponentToken marks a component token as revoked.
	RevokeComponentToken(ctx context.Context, keyID string, revokedAt time.Time) error

	// UpdateComponentTokenLastUsed records successful use of a component token.
	UpdateComponentTokenLastUsed(ctx context.Context, keyID string, lastUsedAt time.Time) error

	// ListComponentTokenMetadata returns token metadata without plaintext or token hashes.
	ListComponentTokenMetadata(ctx context.Context) ([]domain.ComponentTokenMetadata, error)
}
