package out

import "context"

// SecretProvider defines the contract for retrieving secrets.
type SecretProvider interface {
	// Name returns the provider name (e.g., "pass", "sops").
	Name() string

	// GetSecret retrieves a secret by key.
	GetSecret(ctx context.Context, key string) (string, error)

	// IsAvailable checks if this provider is available in the current environment.
	IsAvailable() bool
}
