package out

// AttachmentConfigSnapshot holds a consistent point-in-time snapshot of
// attachment and network group configuration.
type AttachmentConfigSnapshot struct {
	Attachments   map[string][]string // domain/group -> []image
	NetworkGroups map[string][]string // group -> []domain
}

// AttachmentConfigProvider is a read-only view into the live config service
// for attachment and network group resolution. It decouples the container
// service from the full ConfigService interface while ensuring fresh reads
// at decision time (deploy, restart).
//
// Implementations must return deep copies of the underlying data so that
// callers cannot mutate provider state. Each call must produce an independent
// map with independently allocated slices.
type AttachmentConfigProvider interface {
	// GetAttachmentConfig returns a consistent snapshot of attachments and
	// network groups, read under a single lock to prevent cross-field races.
	GetAttachmentConfig() AttachmentConfigSnapshot

	// GetNetworkGroups returns a deep copy of the current network group config: group -> []domain.
	GetNetworkGroups() map[string][]string
}
