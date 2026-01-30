// Package domainsecrets implements the DomainSecretStore adapter using filesystem-based env files.
package domainsecrets

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
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

	data, err := os.ReadFile(envFile)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("failed to read env file: %w", err)
	}

	secrets, err := domain.ParseEnvData(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse env file: %w", err)
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

// getEnvFilePath converts a domain to its env file path.
// This must match the naming convention in envloader.FileLoader.getEnvFilePath.
//
// SECURITY: Validates domain and ensures the resulting path stays within envDir.
func (s *FileStore) getEnvFilePath(domainName string) string {
	safeDomain, err := domain.SanitizeDomainForEnvFile(domainName)
	if err != nil {
		s.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "domainsecrets").
			Str("domain", domainName).
			Err(err).
			Msg("rejected invalid domain")
		return ""
	}

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

// ListAttachmentKeys finds attachment env files for a domain and returns their keys.
// Attachment env files follow the naming pattern: gordon-{sanitized-domain}-{service}.env
func (s *FileStore) ListAttachmentKeys(domainName string) ([]out.AttachmentSecrets, error) {
	// Sanitize domain the same way container service does
	sanitized := sanitizeDomainForContainer(domainName)
	prefix := "gordon-" + sanitized + "-"

	// List all files in env directory
	entries, err := os.ReadDir(s.envDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read env directory: %w", err)
	}

	var results []out.AttachmentSecrets
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Check if this is an attachment file for the domain
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".env") {
			// Extract service name from filename
			// e.g., "gordon-git-bnema-dev-gitea-postgres.env" → "gitea-postgres"
			serviceName := strings.TrimPrefix(name, prefix)
			serviceName = strings.TrimSuffix(serviceName, ".env")
			if serviceName == "" {
				continue
			}

			// The full container name is the filename without .env
			containerName := strings.TrimSuffix(name, ".env")

			// Read keys from this file using existing method
			// Note: We use the container name directly since it matches the env file naming
			keys, err := s.listKeysFromFile(filepath.Join(s.envDir, name))
			if err != nil {
				s.log.Warn().
					Err(err).
					Str("file", name).
					Str("domain", domainName).
					Msg("failed to read attachment secrets file")
				continue
			}

			if len(keys) > 0 {
				results = append(results, out.AttachmentSecrets{
					Service: containerName,
					Keys:    keys,
				})
			}
		}
	}

	return results, nil
}

// sanitizeDomainForContainer matches the sanitizeName function in container service.
func sanitizeDomainForContainer(domain string) string {
	result := strings.ReplaceAll(domain, ".", "-")
	result = strings.ReplaceAll(result, ":", "-")
	result = strings.ReplaceAll(result, "/", "-")
	return result
}

// listKeysFromFile reads secret keys from a specific env file path.
func (s *FileStore) listKeysFromFile(filePath string) ([]string, error) {
	file, err := os.Open(filePath)
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
