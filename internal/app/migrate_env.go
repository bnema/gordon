package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/domain"
)

func migrateEnvFilesToPass(envDir string, passStore *domainsecrets.PassStore, log zerowrap.Logger) error {
	entries, err := readEnvDir(envDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isPlainEnvFile(name) {
			continue
		}
		if err := migrateEnvFile(envDir, name, passStore, log); err != nil {
			return err
		}
	}

	if err := migrateAttachmentEnvFilesToPass(envDir, passStore, log); err != nil {
		return err
	}

	return nil
}

func readEnvDir(envDir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(envDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read env directory: %w", err)
	}
	return entries, nil
}

func isPlainEnvFile(name string) bool {
	if !strings.HasSuffix(name, ".env") || strings.HasSuffix(name, ".env.migrated") {
		return false
	}
	// Skip attachment env files: gordon-<sanitized-domain>-<service>.env
	if strings.HasPrefix(name, "gordon-") {
		return false
	}
	return true
}

func migrateEnvFile(envDir, name string, passStore *domainsecrets.PassStore, log zerowrap.Logger) error {
	filePath := filepath.Join(envDir, name)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read env file %s: %w", filePath, err)
	}

	domainName := strings.TrimSuffix(name, ".env")
	secrets, err := domain.ParseEnvData(data)
	if err != nil {
		return fmt.Errorf("failed to parse env file %s: %w", filePath, err)
	}
	if len(secrets) == 0 {
		log.Info().
			Str("file", filePath).
			Msg("no secrets found in env file; skipping migration")
		return nil
	}

	existingKeys, err := passStore.ListKeys(domainName)
	if err != nil {
		return fmt.Errorf("failed to read pass keys for %s: %w", domainName, err)
	}

	if len(existingKeys) > 0 {
		return fmt.Errorf("%w: pass secrets already exist for %s (found %d keys); refusing to delete plaintext env file automatically", domain.ErrSecretsAlreadyExist, domainName, len(existingKeys))
	}
	if err := passStore.Set(domainName, secrets); err != nil {
		return fmt.Errorf("failed to migrate secrets for %s: %w", domainName, err)
	}

	if err := os.Remove(filePath); err != nil {
		// Check if the stored secrets match what we just wrote
		storedSecrets, getErr := passStore.GetAll(domainName)
		if getErr == nil && secretsMatch(storedSecrets, secrets) {
			// Migration was successful, removal failed but secrets are stored
			log.Warn().
				Str("file", filePath).
				Str("domain", domainName).
				Msg("secrets migrated successfully but failed to remove original file; treating as success")
			return nil
		}
		// Attempt rollback: remove the pass entry we just wrote
		rollbackKeys, listErr := passStore.ListKeys(domainName)
		if listErr == nil {
			for _, key := range rollbackKeys {
				if delErr := passStore.Delete(domainName, key); delErr != nil {
					log.Error().
						Err(delErr).
						Str("domain", domainName).
						Str("key", key).
						Msg("failed to rollback pass entry after file removal failure")
				}
			}
		}
		return fmt.Errorf("failed to remove migrated env file %s: %w", filePath, err)
	}

	log.Info().
		Int(zerowrap.FieldCount, len(secrets)).
		Str("domain", domainName).
		Msg("migrated secrets for domain from plain text to pass and removed original env file")

	return nil
}

func migrateAttachmentEnvFilesToPass(envDir string, passStore *domainsecrets.PassStore, log zerowrap.Logger) error {
	entries, err := readEnvDir(envDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !isAttachmentEnvFile(name) {
			continue
		}
		if err := migrateAttachmentEnvFile(envDir, name, passStore, log); err != nil {
			return err
		}
	}

	return nil
}

func isAttachmentEnvFile(name string) bool {
	if !strings.HasSuffix(name, ".env") || strings.HasSuffix(name, ".env.migrated") {
		return false
	}
	return strings.HasPrefix(name, "gordon-")
}

func migrateAttachmentEnvFile(envDir, name string, passStore *domainsecrets.PassStore, log zerowrap.Logger) error {
	filePath := filepath.Join(envDir, name)
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read attachment env file %s: %w", filePath, err)
	}

	containerName := extractContainerNameFromAttachmentFile(name)
	secrets, err := domain.ParseEnvData(data)
	if err != nil {
		return fmt.Errorf("failed to parse attachment env file %s: %w", filePath, err)
	}
	if len(secrets) == 0 {
		log.Info().
			Str("file", filePath).
			Msg("no secrets found in attachment env file; skipping migration")
		return nil
	}

	existingSecrets, err := passStore.GetAllAttachment(containerName)
	if err != nil {
		return fmt.Errorf("failed to read pass secrets for attachment %s: %w", containerName, err)
	}

	existingKeys := make([]string, 0, len(existingSecrets))
	for key := range existingSecrets {
		existingKeys = append(existingKeys, key)
	}

	if len(existingKeys) > 0 {
		return fmt.Errorf("%w: pass secrets already exist for attachment %s (found %d keys); refusing to delete plaintext env file automatically", domain.ErrSecretsAlreadyExist, containerName, len(existingKeys))
	}
	if err := passStore.SetAttachment(containerName, secrets); err != nil {
		return fmt.Errorf("failed to migrate attachment secrets for %s: %w", containerName, err)
	}

	if err := os.Remove(filePath); err != nil {
		// Check if the stored secrets match what we just wrote
		storedSecrets, getErr := passStore.GetAllAttachment(containerName)
		if getErr == nil && secretsMatch(storedSecrets, secrets) {
			// Migration was successful, removal failed but secrets are stored
			log.Warn().
				Str("file", filePath).
				Str("container", containerName).
				Msg("attachment secrets migrated successfully but failed to remove original file; treating as success")
			return nil
		}
		// Attempt rollback: remove the pass entry we just wrote
		rollbackSecrets, listErr := passStore.GetAllAttachment(containerName)
		if listErr == nil {
			for key := range rollbackSecrets {
				if delErr := passStore.DeleteAttachment(containerName, key); delErr != nil {
					log.Error().
						Err(delErr).
						Str("container", containerName).
						Str("key", key).
						Msg("failed to rollback pass entry after file removal failure")
				}
			}
		}
		return fmt.Errorf("failed to remove migrated attachment env file %s: %w", filePath, err)
	}

	log.Info().
		Int(zerowrap.FieldCount, len(secrets)).
		Str("container", containerName).
		Str("file", filePath).
		Msg("migrated attachment secrets to pass and removed original env file")

	return nil
}

func extractContainerNameFromAttachmentFile(filename string) string {
	name := strings.TrimSuffix(filename, ".env")
	return name
}

func secretsMatch(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
