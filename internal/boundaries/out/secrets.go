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

// SecretWriter defines the contract for writing app secret values by path.
// v2.50 app secrets live in pass at gordon/apps/<app>/<service>/<name>;
// values cross this boundary only on the explicit SetSecrets path.
type SecretWriter interface {
	// SetSecret writes one secret value by path.
	SetSecret(ctx context.Context, path, value string) error

	// DeleteSecret removes one secret value by path.
	DeleteSecret(ctx context.Context, path string) error
}
