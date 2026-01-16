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
func (h *Handler) domainToEnvFile(domain string) string {
	// Replace special characters with underscores (matches envloader)
	filename := strings.ReplaceAll(domain, ".", "_")
	filename = strings.ReplaceAll(filename, ":", "_")
	filename = strings.ReplaceAll(filename, "/", "_")
	return filepath.Join(h.envDir, filename+".env")
}

// listSecrets returns the list of secret keys for a domain (not values).
func (h *Handler) listSecrets(domain string) ([]string, error) {
	envFile := h.domainToEnvFile(domain)

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
	envFile := h.domainToEnvFile(domain)

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

// writeSecrets writes all secrets to the domain's env file.
func (h *Handler) writeSecrets(domain string, secrets map[string]string) (err error) {
	envFile := h.domainToEnvFile(domain)

	// Create/truncate file with secure permissions
	file, err := os.OpenFile(envFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create env file: %w", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("failed to close env file: %w", cerr)
		}
	}()

	// Write header comment
	if _, err := fmt.Fprintf(file, "# Environment variables for %s\n", domain); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}
	if _, err := fmt.Fprintf(file, "# Managed by Gordon admin API\n\n"); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Write each secret
	for key, value := range secrets {
		if _, err := fmt.Fprintf(file, "%s=%s\n", key, value); err != nil {
			return fmt.Errorf("failed to write secret %s: %w", key, err)
		}
	}

	return nil
}
