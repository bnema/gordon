package validation

import "strings"

// ParseImageReference parses an image reference into name and tag/digest.
// Supports formats:
//   - image:tag (default tag is "latest")
//   - image@sha256:... (digest)
//   - image (defaults to "latest")
//   - registry.example.com/image:tag
//   - registry.example.com/image@sha256:...
//
// Returns:
//   - name: the image name (including registry if specified)
//   - reference: the tag (e.g., "latest") or digest (e.g., "sha256:...")
func ParseImageReference(imageRef string) (string, string) {
	// Check for digest reference (@sha256:...)
	if idx := strings.Index(imageRef, "@sha256:"); idx != -1 {
		return imageRef[:idx], imageRef[idx+1:] // Return name and full digest
	}

	// Check for tag reference (:tag)
	if idx := strings.Index(imageRef, ":"); idx != -1 {
		// Check if this is actually a port number (e.g., registry:5000/image)
		// by looking for a slash before the colon
		if slashIdx := strings.Index(imageRef, "/"); slashIdx != -1 && slashIdx > idx {
			// Slash comes after colon, so colon is part of registry port
			// Look for another colon for the tag
			if tagIdx := strings.Index(imageRef[slashIdx:], ":"); tagIdx != -1 {
				actualTagIdx := slashIdx + tagIdx
				return imageRef[:actualTagIdx], imageRef[actualTagIdx+1:]
			}
			return imageRef, "latest"
		}
		return imageRef[:idx], imageRef[idx+1:] // Return name and tag
	}

	// Default to "latest" tag
	return imageRef, "latest"
}
