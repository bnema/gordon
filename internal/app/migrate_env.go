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
	return strings.HasSuffix(name, ".env") && !strings.HasSuffix(name, ".env.migrated")
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

	existingKeys, err := passStore.ListKeys(domainName)
	if err != nil {
		return fmt.Errorf("failed to read pass keys for %s: %w", domainName, err)
	}

	missing := missingSecrets(existingKeys, secrets)
	if len(missing) > 0 {
		if err := passStore.Set(domainName, missing); err != nil {
			return fmt.Errorf("failed to migrate secrets for %s: %w", domainName, err)
		}
	}

	migratedPath := filePath + ".migrated"
	if err := os.Rename(filePath, migratedPath); err != nil {
		return fmt.Errorf("failed to rename env file %s: %w", filePath, err)
	}

	log.Info().
		Int(zerowrap.FieldCount, len(missing)).
		Str("domain", domainName).
		Msg("migrated secrets for domain from plain text to pass")
	log.Info().
		Str("file", migratedPath).
		Msg("original file renamed to .env.migrated - you can safely remove it")

	return nil
}

func missingSecrets(existingKeys []string, secrets map[string]string) map[string]string {
	if len(secrets) == 0 {
		return nil
	}

	existingSet := make(map[string]struct{}, len(existingKeys))
	for _, key := range existingKeys {
		existingSet[key] = struct{}{}
	}

	missing := make(map[string]string)
	for key, value := range secrets {
		if _, exists := existingSet[key]; exists {
			continue
		}
		missing[key] = value
	}

	if len(missing) == 0 {
		return nil
	}

	return missing
}
