package container

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
)

// generateVolumeName creates a deterministic volume name for a domain and path
func generateVolumeName(prefix, domain, path string) string {
	// Sanitize domain name for use in volume name
	sanitizedDomain := sanitizeDomainForVolume(domain)

	// Create a short hash of the path to handle long paths and special characters
	hasher := sha256.New()
	hasher.Write([]byte(path))
	pathHash := fmt.Sprintf("%x", hasher.Sum(nil))[:8]

	return fmt.Sprintf("%s-%s-%s", prefix, sanitizedDomain, pathHash)
}

// sanitizeDomainForVolume converts domain to volume-safe format
func sanitizeDomainForVolume(domain string) string {
	// Replace dots and other special characters with hyphens
	reg := regexp.MustCompile(`[^a-zA-Z0-9\-]`)
	sanitized := reg.ReplaceAllString(domain, "-")

	// Remove leading/trailing hyphens and convert to lowercase
	sanitized = strings.Trim(sanitized, "-")
	sanitized = strings.ToLower(sanitized)

	return sanitized
}

// normalizeImageRef normalizes image references for comparison
func normalizeImageRef(imageRef string) string {
	// Split image and tag
	parts := strings.Split(imageRef, ":")
	image := parts[0]
	tag := "latest"
	if len(parts) > 1 {
		tag = parts[1]
	}

	// Normalize Docker Hub references
	if !strings.Contains(image, "/") {
		// Official library image (e.g., "nginx" -> "docker.io/library/nginx")
		image = "docker.io/library/" + image
	} else if strings.Count(image, "/") == 1 && !strings.Contains(strings.Split(image, "/")[0], ".") {
		// User repository (e.g., "user/repo" -> "docker.io/user/repo")
		image = "docker.io/" + image
	}

	return image + ":" + tag
}

// mergeEnvironmentVariables merges Dockerfile ENV directives with user-provided env vars
// User-provided env vars take precedence over Dockerfile ENV directives
func mergeEnvironmentVariables(dockerfileEnv, userEnv []string) []string {
	// Create a map to store environment variables by key
	envMap := make(map[string]string)

	// First, add Dockerfile ENV directives
	for _, env := range dockerfileEnv {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Then, add user-provided env vars (these override Dockerfile ENV)
	for _, env := range userEnv {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// Convert back to slice format
	var result []string
	for key, value := range envMap {
		result = append(result, fmt.Sprintf("%s=%s", key, value))
	}

	return result
}
