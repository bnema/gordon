package out

import (
	"context"

	"github.com/bnema/gordon/internal/domain"
)

// TokenStore defines the contract for storing and managing authentication tokens.
// Implementations may use different backends (pass, sops, unsafe file storage).
type TokenStore interface {
	// SaveToken stores a token JWT and metadata.
	SaveToken(ctx context.Context, token *domain.Token, jwt string) error

	// GetToken retrieves token JWT by subject.
	GetToken(ctx context.Context, subject string) (string, *domain.Token, error)

	// ListTokens returns all stored tokens.
	ListTokens(ctx context.Context) ([]domain.Token, error)

	// Revoke adds token ID to revocation list.
	Revoke(ctx context.Context, tokenID string) error

	// IsRevoked checks if token ID is in revocation list.
	IsRevoked(ctx context.Context, tokenID string) (bool, error)

	// DeleteToken removes token from store.
	DeleteToken(ctx context.Context, subject string) error

	// UpdateTokenExpiry updates the JWT and expiry/LastExtendedAt metadata for an existing token.
	// Used by token sliding expiry to re-sign tokens without changing the JTI.
	UpdateTokenExpiry(ctx context.Context, token *domain.Token, newJWT string) error
}
