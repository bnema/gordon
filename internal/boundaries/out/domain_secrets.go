package out

// DomainSecretStore defines the contract for managing domain-scoped secrets.
// These are environment variables stored per-domain for container injection.
type DomainSecretStore interface {
	// ListKeys returns the list of secret keys for a domain (not values).
	ListKeys(domain string) ([]string, error)

	// GetAll returns all secrets for a domain as a key-value map.
	GetAll(domain string) (map[string]string, error)

	// Set sets or updates multiple secrets for a domain, merging with existing.
	Set(domain string, secrets map[string]string) error

	// Delete removes a specific secret key from a domain.
	Delete(domain, key string) error
}
