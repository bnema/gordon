package in

import (
	"context"

	"gordon/internal/boundaries/out"
)

// SecretService defines the contract for managing domain-scoped secrets.
// These are environment variables stored per-domain for container injection.
type SecretService interface {
	// ListKeys returns the list of secret keys for a domain (not values).
	// Returns an error if the domain is invalid.
	ListKeys(ctx context.Context, domain string) ([]string, error)

	// ListKeysWithAttachments returns the list of secret keys for a domain
	// along with any attachment secrets for containers associated with the domain.
	ListKeysWithAttachments(ctx context.Context, domain string) ([]string, []out.AttachmentSecrets, error)

	// GetAll returns all secrets for a domain as a key-value map.
	// Returns an error if the domain is invalid.
	GetAll(ctx context.Context, domain string) (map[string]string, error)

	// Set sets or updates multiple secrets for a domain, merging with existing.
	// Returns an error if the domain is invalid.
	Set(ctx context.Context, domain string, secrets map[string]string) error

	// Delete removes a specific secret key from a domain.
	// Returns an error if the domain is invalid.
	Delete(ctx context.Context, domain, key string) error
}
