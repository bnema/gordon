package admin

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// domainToEnvFile converts a domain to its env file path.
// Example: app.mydomain.com -> app_mydomain_com.env
// Must match the naming convention in envloader.FileLoader.getEnvFilePath
// Returns an error if the domain contains path traversal attempts or is invalid.
func (h *Handler) domainToEnvFile(domain string) (string, error) {
	// Reject path traversal attempts
	if strings.Contains(domain, "..") {
		return "", fmt.Errorf("invalid domain: path traversal not allowed")
	}

	// Validate domain length (max DNS name length is 253)
	if len(domain) == 0 || len(domain) > 253 {
		return "", fmt.Errorf("invalid domain length")
	}

	// Replace special characters with underscores (matches envloader)
	filename := strings.ReplaceAll(domain, ".", "_")
	filename = strings.ReplaceAll(filename, ":", "_")
	filename = strings.ReplaceAll(filename, "/", "_")

	fullPath := filepath.Join(h.envDir, filename+".env")

	// Verify path stays within envDir after cleaning
	cleanPath := filepath.Clean(fullPath)
	cleanEnvDir := filepath.Clean(h.envDir)
	if !strings.HasPrefix(cleanPath, cleanEnvDir+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid domain: path escapes env directory")
	}

	return cleanPath, nil
}

// listSecrets returns the list of secret keys for a domain (not values).
func (h *Handler) listSecrets(domain string) ([]string, error) {
	envFile, err := h.domainToEnvFile(domain)
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

// getSecrets returns all secrets for a domain as a map.
func (h *Handler) getSecrets(domain string) (map[string]string, error) {
	envFile, err := h.domainToEnvFile(domain)
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

// setSecrets sets multiple secrets for a domain, merging with existing.
func (h *Handler) setSecrets(domain string, newSecrets map[string]string) error {
	// Ensure env directory exists
	if err := os.MkdirAll(h.envDir, 0700); err != nil {
		return fmt.Errorf("failed to create env directory: %w", err)
	}

	// Read existing secrets
	existing, err := h.getSecrets(domain)
	if err != nil {
		return err
	}

	// Merge new secrets with existing
	for key, value := range newSecrets {
		existing[key] = value
	}

	// Write back
	return h.writeSecrets(domain, existing)
}

// deleteSecret removes a secret from a domain's env file.
func (h *Handler) deleteSecret(domain, key string) error {
	// Read existing secrets
	existing, err := h.getSecrets(domain)
	if err != nil {
		return err
	}

	// Remove the key
	delete(existing, key)

	// Write back
	return h.writeSecrets(domain, existing)
}

// writeSecrets writes all secrets to the domain's env file atomically.
// It writes to a temporary file first, syncs it, then renames to the final path.
func (h *Handler) writeSecrets(domain string, secrets map[string]string) error {
	envFile, err := h.domainToEnvFile(domain)
	if err != nil {
		return err
	}

	// Write to temp file for atomic operation
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
