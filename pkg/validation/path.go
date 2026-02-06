// Package validation provides input validation functions for security-critical operations.
// These functions implement defense-in-depth against path traversal and injection attacks.
package validation

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Repository name validation per Docker spec:
// - Lowercase letters, digits, and separators (., _, -)
// - Separators must not be adjacent and cannot start/end the name
// - Allows nested paths like "myorg/myapp"
var repoNameRegex = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

// Reference (tag) validation per Docker spec:
// - Case-sensitive alphanumeric (both uppercase and lowercase allowed)
// - Dots, underscores, and hyphens allowed after first character
// - Must start with an alphanumeric character
// - Max 128 characters
var referenceRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// Digest validation for content-addressable storage:
// - Format: algorithm:hex
// - Supported algorithms: sha256 (64 hex chars), sha512 (128 hex chars)
var digestRegex = regexp.MustCompile(`^(sha256:[a-f0-9]{64}|sha512:[a-f0-9]{128})$`)

// UUID validation for blob uploads:
// - Format: standard UUID v4 (e.g., 550e8400-e29b-41d4-a716-446655440000)
var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// MaxRepositoryNameLength is the maximum allowed length for repository names.
const MaxRepositoryNameLength = 256

// ValidateRepositoryName validates a Docker repository name.
// Returns an error if the name is invalid or could enable path traversal.
func ValidateRepositoryName(name string) error {
	if name == "" {
		return fmt.Errorf("repository name cannot be empty")
	}

	if len(name) > MaxRepositoryNameLength {
		return fmt.Errorf("repository name too long: %d chars (max %d)", len(name), MaxRepositoryNameLength)
	}

	// Check for path traversal attempts
	if strings.Contains(name, "..") {
		return fmt.Errorf("repository name contains path traversal sequence")
	}

	// Validate format
	if !repoNameRegex.MatchString(name) {
		return fmt.Errorf("invalid repository name format: must contain only lowercase letters, digits, and separators (., _, -)")
	}

	return nil
}

// ValidateReference validates a Docker tag or reference.
// Also accepts digest format for manifest references by digest.
func ValidateReference(reference string) error {
	if reference == "" {
		return fmt.Errorf("reference cannot be empty")
	}

	// Check for path traversal attempts
	if strings.Contains(reference, "..") {
		return fmt.Errorf("reference contains path traversal sequence")
	}

	// Allow digest format (sha256:... or sha512:...)
	if digestRegex.MatchString(reference) {
		return nil
	}

	// Validate tag format
	if !referenceRegex.MatchString(reference) {
		return fmt.Errorf("invalid reference format: must be a valid tag or digest")
	}

	return nil
}

// ValidateDigest validates a Docker content digest.
// Format must be algorithm:hex where algorithm is sha256 or sha512.
func ValidateDigest(digest string) error {
	if digest == "" {
		return fmt.Errorf("digest cannot be empty")
	}

	// Check for path traversal attempts
	if strings.Contains(digest, "..") {
		return fmt.Errorf("digest contains path traversal sequence")
	}

	if !digestRegex.MatchString(digest) {
		return fmt.Errorf("invalid digest format: must be sha256:<64 hex chars> or sha512:<128 hex chars>")
	}

	return nil
}

// IsDigest checks if a string is a valid Docker content digest.
// Returns true if the string is a valid digest format.
func IsDigest(digest string) bool {
	return ValidateDigest(digest) == nil
}

// ValidateUUID validates a blob upload UUID.
// UUIDs are server-generated but still validated for safety.
func ValidateUUID(uuid string) error {
	if uuid == "" {
		return fmt.Errorf("UUID cannot be empty")
	}

	// Check for path traversal attempts
	if strings.Contains(uuid, "..") {
		return fmt.Errorf("UUID contains path traversal sequence")
	}

	if !uuidRegex.MatchString(uuid) {
		return fmt.Errorf("invalid UUID format")
	}

	return nil
}

// ValidatePath sanitizes and validates a path component to prevent traversal attacks.
// Returns the cleaned path or an error if the path is unsafe.
func ValidatePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	// Clean the path to normalize it
	cleanPath := filepath.Clean(path)

	// Check for path traversal after cleaning
	if strings.Contains(cleanPath, "..") {
		return "", fmt.Errorf("path traversal not allowed")
	}

	// Reject absolute paths
	if filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("absolute paths not allowed")
	}

	return cleanPath, nil
}

// ValidatePathWithinRoot validates that a constructed path stays within the root directory.
// This provides defense-in-depth after filepath.Join operations.
func ValidatePathWithinRoot(rootDir, fullPath string) error {
	cleanRoot := filepath.Clean(rootDir)
	cleanPath := filepath.Clean(fullPath)

	// Ensure the path starts with the root directory
	if !strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator)) && cleanPath != cleanRoot {
		return fmt.Errorf("path escapes root directory")
	}

	return nil
}
