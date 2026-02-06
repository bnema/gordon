package out

// AttachmentSecrets represents secrets for an attachment container.
type AttachmentSecrets struct {
	// Service is the attachment service name (e.g., "gitea-postgres")
	Service string
	// Keys is the list of secret keys for this attachment
	Keys []string
}

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

	// SetAttachment sets or updates multiple secrets for an attachment container.
	SetAttachment(containerName string, secrets map[string]string) error

	// GetAllAttachment returns all secrets for an attachment container as a key-value map.
	GetAllAttachment(containerName string) (map[string]string, error)

	// DeleteAttachment removes a specific secret key from an attachment container.
	DeleteAttachment(containerName, key string) error

	// ListAttachmentKeys finds and returns secret keys for attachment containers
	// associated with the given domain. Returns a list of AttachmentSecrets, one
	// for each attachment that has secrets configured.
	ListAttachmentKeys(domain string) ([]AttachmentSecrets, error)
}
