package domain

import (
	"encoding/base64"
	"regexp"
	"strings"
)

// envDomainRegex validates domain names used for env-backed secret storage.
// Allows: alphanumeric, dots, hyphens, colons (for ports), and forward slashes (for paths).
var envDomainRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.:/-]*[a-zA-Z0-9]$|^[a-zA-Z0-9]$`)

// EnvStorageKey is the domain-level storage identifier used for env-backed secrets.
// It is deterministic, collision-resistant, and safe to use in file names.
type EnvStorageKey string

// NewEnvStorageKey validates a route domain and returns the storage-safe identifier
// used by env/secrets adapters. The identifier is reversible and unique for the input.
func NewEnvStorageKey(domainName string) (EnvStorageKey, error) {
	if err := ValidateEnvStorageDomain(domainName); err != nil {
		return "", err
	}

	return EnvStorageKey(base64.RawURLEncoding.EncodeToString([]byte(domainName))), nil
}

// ValidateEnvStorageDomain validates domains used for env-backed secret storage.
func ValidateEnvStorageDomain(domainName string) error {
	if domainName == "" {
		return ErrPathTraversal
	}

	if strings.Contains(domainName, "..") {
		return ErrPathTraversal
	}

	if !envDomainRegex.MatchString(domainName) {
		return ErrPathTraversal
	}

	return nil
}

// FileName returns the on-disk env filename for this storage key.
func (k EnvStorageKey) FileName() string {
	return string(k) + ".env"
}
