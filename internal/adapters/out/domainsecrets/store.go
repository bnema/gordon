// Package domainsecrets implements the DomainSecretStore adapter using filesystem-based env files.
package domainsecrets

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/domain"
)

// FileStore implements the DomainSecretStore interface using filesystem-based env files.
// This adapter is responsible only for file I/O operations; domain validation
// should be performed by the use case layer before calling these methods.
type FileStore struct {
	envDir string
	log    zerowrap.Logger
}

// NewFileStore creates a new file-based domain secret store.
func NewFileStore(envDir string, log zerowrap.Logger) (*FileStore, error) {
	// Ensure env directory exists
	if err := os.MkdirAll(envDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create env directory %s: %w", envDir, err)
	}

	log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "domainsecrets").
		Str("env_dir", envDir).
		Msg("domain secret store initialized")

	return &FileStore{
		envDir: envDir,
		log:    log,
	}, nil
}

// ListKeys returns the list of secret keys for a domain (not values).
func (s *FileStore) ListKeys(domainName string) ([]string, error) {
	envFile, err := s.validateEnvFilePath(domainName)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(envFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to open env file: %w", err)
	}
	defer file.Close()

	var keys []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Extract key from KEY=value
		if idx := strings.Index(line, "="); idx > 0 {
			keys = append(keys, line[:idx])
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read env file: %w", err)
	}

	return keys, nil
}

// GetAll returns all secrets for a domain as a key-value map.
func (s *FileStore) GetAll(domainName string) (map[string]string, error) {
	envFile, err := s.validateEnvFilePath(domainName)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(envFile)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to open env file: %w", err)
	}
	defer file.Close()

	secrets := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Parse KEY=value
		if idx := strings.Index(line, "="); idx > 0 {
			key := line[:idx]
			value := line[idx+1:]
			secrets[key] = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read env file: %w", err)
	}

	return secrets, nil
}

// Set sets or updates multiple secrets for a domain, merging with existing.
func (s *FileStore) Set(domainName string, secrets map[string]string) error {
	// Validate domain first
	if _, err := s.validateEnvFilePath(domainName); err != nil {
		return err
	}

	// Ensure env directory exists
	if err := os.MkdirAll(s.envDir, 0700); err != nil {
		return fmt.Errorf("failed to create env directory: %w", err)
	}

	// Read existing secrets
	existing, err := s.GetAll(domainName)
	if err != nil {
		return err
	}

	// Merge new secrets with existing
	for key, value := range secrets {
		existing[key] = value
	}

	// Write back atomically
	return s.writeSecretsAtomic(domainName, existing)
}

// Delete removes a specific secret key from a domain.
func (s *FileStore) Delete(domainName, key string) error {
	// Validate domain first
	if _, err := s.validateEnvFilePath(domainName); err != nil {
		return err
	}

	// Read existing secrets
	existing, err := s.GetAll(domainName)
	if err != nil {
		return err
	}

	// Remove the key
	delete(existing, key)

	// Write back atomically
	return s.writeSecretsAtomic(domainName, existing)
}

// writeSecretsAtomic writes all secrets to the domain's env file atomically.
// It writes to a temporary file first, syncs it, then renames to the final path.
func (s *FileStore) writeSecretsAtomic(domainName string, secrets map[string]string) error {
	envFile, err := s.validateEnvFilePath(domainName)
	if err != nil {
		return err
	}
	tmpFile := envFile + ".tmp"

	file, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create temp env file: %w", err)
	}

	// Write header comment
	if _, err := fmt.Fprintf(file, "# Environment variables for %s\n", domainName); err != nil {
		file.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("failed to write header: %w", err)
	}
	if _, err := fmt.Fprintf(file, "# Managed by Gordon admin API\n\n"); err != nil {
		file.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Write each secret
	for key, value := range secrets {
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, value); err != nil {
			file.Close()
			os.Remove(tmpFile)
			return fmt.Errorf("failed to write secret %s: %w", key, err)
		}
	}

	// Sync to ensure data is on disk before rename
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("failed to sync env file: %w", err)
	}

	if err := file.Close(); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to close env file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile, envFile); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to rename env file: %w", err)
	}

	return nil
}

// domainRegex validates domain names to prevent path injection.
// Allows: alphanumeric, dots, hyphens, colons (for ports), and forward slashes (for paths).
var domainRegex = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*[a-zA-Z0-9]$|^[a-zA-Z0-9]$`)

// getEnvFilePath converts a domain to its env file path.
// This must match the naming convention in envloader.FileLoader.getEnvFilePath.
//
// SECURITY: Validates domain and ensures the resulting path stays within envDir.
func (s *FileStore) getEnvFilePath(domainName string) string {
	// SECURITY: Reject domains that look like path traversal attempts
	if strings.Contains(domainName, "..") {
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "domainsecrets").
			Str("domain", domainName).
			Msg("rejected domain with path traversal sequence")
		return ""
	}

	// SECURITY: Validate domain format
	if !domainRegex.MatchString(domainName) {
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "domainsecrets").
			Str("domain", domainName).
			Msg("rejected invalid domain format")
		return ""
	}

	// Create domain-safe filename (replace dots and other chars with underscores)
	safeDomain := strings.ReplaceAll(domainName, ".", "_")
	safeDomain = strings.ReplaceAll(safeDomain, ":", "_")
	safeDomain = strings.ReplaceAll(safeDomain, "/", "_")

	fullPath := filepath.Join(s.envDir, safeDomain+".env")

	// SECURITY: Final validation - ensure path stays within envDir
	cleanPath := filepath.Clean(fullPath)
	cleanEnvDir := filepath.Clean(s.envDir)
	if !strings.HasPrefix(cleanPath, cleanEnvDir+string(filepath.Separator)) {
		s.log.Error().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "domainsecrets").
			Str("domain", domainName).
			Str("attempted_path", fullPath).
			Msg("path traversal attempt blocked - path escapes env directory")
		return ""
	}

	return fullPath
}

// validateEnvFilePath validates that a domain produces a valid env file path.
// Returns an error if the domain is invalid or would result in path traversal.
func (s *FileStore) validateEnvFilePath(domainName string) (string, error) {
	path := s.getEnvFilePath(domainName)
	if path == "" {
		return "", domain.ErrPathTraversal
	}
	return path, nil
}
