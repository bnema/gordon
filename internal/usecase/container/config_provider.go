package container

// AttachmentConfigProvider is a read-only view into the live config service
// for attachment and network group resolution. It decouples the container
// service from the full ConfigService interface while ensuring fresh reads
// at decision time (deploy, restart).
type AttachmentConfigProvider interface {
	// GetAttachments returns the current attachment config: domain/group → []image.
	GetAttachments() map[string][]string

	// GetNetworkGroups returns the current network group config: group → []domain.
	GetNetworkGroups() map[string][]string
}
