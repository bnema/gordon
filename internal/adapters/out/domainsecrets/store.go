// Package domainsecrets implements the DomainSecretStore adapter using filesystem-based env files.
package domainsecrets

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"
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
func (s *FileStore) ListKeys(domain string) ([]string, error) {
	envFile := s.getEnvFilePath(domain)

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
func (s *FileStore) GetAll(domain string) (map[string]string, error) {
	envFile := s.getEnvFilePath(domain)

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
func (s *FileStore) Set(domain string, secrets map[string]string) error {
	// Ensure env directory exists
	if err := os.MkdirAll(s.envDir, 0700); err != nil {
		return fmt.Errorf("failed to create env directory: %w", err)
	}

	// Read existing secrets
	existing, err := s.GetAll(domain)
	if err != nil {
		return err
	}

	// Merge new secrets with existing
	for key, value := range secrets {
		existing[key] = value
	}

	// Write back atomically
	return s.writeSecretsAtomic(domain, existing)
}

// Delete removes a specific secret key from a domain.
func (s *FileStore) Delete(domain, key string) error {
	// Read existing secrets
	existing, err := s.GetAll(domain)
	if err != nil {
		return err
	}

	// Remove the key
	delete(existing, key)

	// Write back atomically
	return s.writeSecretsAtomic(domain, existing)
}

// writeSecretsAtomic writes all secrets to the domain's env file atomically.
// It writes to a temporary file first, syncs it, then renames to the final path.
func (s *FileStore) writeSecretsAtomic(domain string, secrets map[string]string) error {
	envFile := s.getEnvFilePath(domain)
	tmpFile := envFile + ".tmp"

	file, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create temp env file: %w", err)
	}

	// Write header comment
	if _, err := fmt.Fprintf(file, "# Environment variables for %s\n", domain); err != nil {
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
func (s *FileStore) getEnvFilePath(domain string) string {
	// Create domain-safe filename (replace dots and other chars with underscores)
	safeDomain := strings.ReplaceAll(domain, ".", "_")
	safeDomain = strings.ReplaceAll(safeDomain, ":", "_")
	safeDomain = strings.ReplaceAll(safeDomain, "/", "_")
	return filepath.Join(s.envDir, safeDomain+".env")
}
