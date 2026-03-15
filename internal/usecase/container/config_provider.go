package container

// AttachmentConfigProvider is a read-only view into the live config service
// for attachment and network group resolution. It decouples the container
// service from the full ConfigService interface while ensuring fresh reads
// at decision time (deploy, restart).
//
// Implementations must return deep copies of the underlying data so that
// callers cannot mutate provider state. Each call must produce an independent
// map with independently allocated slices.
type AttachmentConfigProvider interface {
	// GetAttachments returns a deep copy of the current attachment config: domain/group → []image.
	GetAttachments() map[string][]string

	// GetNetworkGroups returns a deep copy of the current network group config: group → []domain.
	GetNetworkGroups() map[string][]string
}

// deepCopyStringMap returns a deep copy of a map[string][]string.
func deepCopyStringMap(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	result := make(map[string][]string, len(m))
	for k, v := range m {
		result[k] = append([]string{}, v...)
	}
	return result
}
