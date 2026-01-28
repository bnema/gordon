package validation

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
	if idx := indexString(imageRef, "@sha256:"); idx != -1 {
		return imageRef[:idx], imageRef[idx+1:] // Return name and full digest
	}

	// Check for tag reference (:tag)
	if idx := indexString(imageRef, ":"); idx != -1 {
		// Check if this is actually a port number (e.g., registry:5000/image)
		// by looking for a slash before the colon
		if slashIdx := indexString(imageRef, "/"); slashIdx != -1 && slashIdx > idx {
			// Slash comes after colon, so colon is part of registry port
			// Look for another colon for the tag
			if tagIdx := indexString(imageRef[slashIdx:], ":"); tagIdx != -1 {
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

// indexString returns the index of the first occurrence of substr in s, or -1 if not found.
// This is a helper function that mirrors strings.Index for testing purposes.
func indexString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
