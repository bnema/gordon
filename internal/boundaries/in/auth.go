package in

import (
	"context"
	"time"

	"github.com/bnema/gordon/internal/domain"
)

// AuthService defines the contract for registry authentication operations.
type AuthService interface {
	// GetAuthType returns the configured authentication type.
	GetAuthType() domain.AuthType

	// IsEnabled returns whether authentication is enabled.
	IsEnabled() bool

	// Password authentication
	// ValidatePassword checks if the username and password are valid.
	ValidatePassword(ctx context.Context, username, password string) bool

	// Token authentication
	// ValidateToken validates a JWT token and returns its claims.
	ValidateToken(ctx context.Context, tokenString string) (*domain.TokenClaims, error)

	// GenerateToken creates a new JWT token for the given subject and stores it.
	// If expiry is 0, the token never expires.
	GenerateToken(ctx context.Context, subject string, scopes []string, expiry time.Duration) (string, error)

	// GenerateAccessToken creates a short-lived JWT for registry access without storing it.
	// Used by /v2/token endpoint for Docker client sessions.
	GenerateAccessToken(ctx context.Context, subject string, scopes []string, expiry time.Duration) (string, error)

	// RevokeToken revokes a token by its ID.
	RevokeToken(ctx context.Context, tokenID string) error

	// RevokeAllTokens revokes all stored tokens.
	RevokeAllTokens(ctx context.Context) (int, error)

	// ListTokens returns all stored tokens.
	ListTokens(ctx context.Context) ([]domain.Token, error)

	// GeneratePasswordHash generates a bcrypt hash for a password.
	GeneratePasswordHash(password string) (string, error)

	// GetAuthStatus returns authentication status from context.
	// Extracts and validates token claims that were set by auth middleware.
	GetAuthStatus(ctx context.Context) (*domain.AuthStatus, error)
}
