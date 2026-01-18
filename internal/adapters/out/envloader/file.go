// Package envloader implements the environment variable loader adapter.
package envloader

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
)

// FileLoader implements the EnvLoader interface using filesystem-based env files.
type FileLoader struct {
	envDir          string
	secretProviders map[string]out.SecretProvider
	log             zerowrap.Logger
}

// NewFileLoader creates a new file-based environment loader.
func NewFileLoader(envDir string, log zerowrap.Logger) (*FileLoader, error) {
	// Ensure env directory exists
	if err := os.MkdirAll(envDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create env directory %s: %w", envDir, err)
	}

	log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "envloader").
		Str("env_dir", envDir).
		Msg("env loader initialized")

	return &FileLoader{
		envDir:          envDir,
		secretProviders: make(map[string]out.SecretProvider),
		log:             log,
	}, nil
}

// RegisterSecretProvider registers a secret provider for resolving secrets.
func (l *FileLoader) RegisterSecretProvider(provider out.SecretProvider) {
	l.secretProviders[provider.Name()] = provider
	l.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "envloader").
		Str("provider", provider.Name()).
		Msg("secret provider registered")
}

// LoadEnv loads environment variables for a given domain.
func (l *FileLoader) LoadEnv(ctx context.Context, domain string) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "envloader",
		zerowrap.FieldAction:  "LoadEnv",
		"domain":              domain,
	})
	log := zerowrap.FromCtx(ctx)

	envVars := []string{}
	envFile := l.getEnvFilePath(domain)

	// Check if env file exists
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		log.Debug().Str("env_file", envFile).Msg("no env file found for route")
		return envVars, nil
	}

	log.Debug().Str("env_file", envFile).Msg("loading env file for route")

	file, err := os.Open(envFile)
	if err != nil {
		return nil, log.WrapErr(err, "failed to open env file")
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=VALUE format
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			log.Warn().
				Int("line", lineNum).
				Str("content", line).
				Msg("invalid env file line format, skipping")
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove quotes if present
		if len(value) >= 2 {
			if (strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"")) ||
				(strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")) {
				value = value[1 : len(value)-1]
			}
		}

		// Resolve secrets if value contains secret syntax
		resolvedValue, err := l.resolveSecrets(ctx, value)
		if err != nil {
			return nil, log.WrapErrWithFields(err, "failed to resolve secret", map[string]any{
				"key":  key,
				"line": lineNum,
			})
		}

		envVars = append(envVars, fmt.Sprintf("%s=%s", key, resolvedValue))
	}

	if err := scanner.Err(); err != nil {
		return nil, log.WrapErr(err, "error reading env file")
	}

	log.Info().Int(zerowrap.FieldCount, len(envVars)).Msg("loaded environment variables for route")

	return envVars, nil
}

// CreateEnvFile creates an empty environment file for a new domain.
func (l *FileLoader) CreateEnvFile(ctx context.Context, domain string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "envloader",
		zerowrap.FieldAction:  "CreateEnvFile",
		"domain":              domain,
	})
	log := zerowrap.FromCtx(ctx)

	envFile := l.getEnvFilePath(domain)

	// Check if file already exists
	if _, err := os.Stat(envFile); err == nil {
		log.Debug().Str("env_file", envFile).Msg("env file already exists, skipping creation")
		return nil
	} else if !os.IsNotExist(err) {
		return log.WrapErr(err, "error checking env file")
	}

	// Create empty env file with helpful comments
	content := fmt.Sprintf(`# Environment variables for route: %s
# Add KEY=VALUE pairs, one per line
# Secrets can be referenced with ${provider:path} syntax

`, domain)

	if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
		return log.WrapErr(err, "failed to create env file")
	}

	log.Info().Str("env_file", envFile).Msg("created empty env file for route")

	return nil
}

// EnvFileExists checks if an environment file exists for a domain.
func (l *FileLoader) EnvFileExists(domain string) bool {
	envFile := l.getEnvFilePath(domain)
	_, err := os.Stat(envFile)
	return err == nil
}

// resolveSecrets resolves any secret references in the value.
// Secret syntax: ${provider:path}
func (l *FileLoader) resolveSecrets(ctx context.Context, value string) (string, error) {
	// Check if value contains secret syntax: ${provider:path}
	if !strings.Contains(value, "${") {
		return value, nil
	}

	result := value
	for {
		start := strings.Index(result, "${")
		if start == -1 {
			break
		}

		end := strings.Index(result[start:], "}")
		if end == -1 {
			return "", fmt.Errorf("unclosed secret syntax in value: %s", value)
		}
		end += start

		secretRef := result[start+2 : end]
		parts := strings.SplitN(secretRef, ":", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid secret syntax: expected 'provider:path', got '%s'", secretRef)
		}

		providerName := parts[0]
		secretPath := parts[1]

		provider, exists := l.secretProviders[providerName]
		if !exists {
			return "", fmt.Errorf("unknown secret provider: %s", providerName)
		}

		secretValue, err := provider.GetSecret(ctx, secretPath)
		if err != nil {
			return "", fmt.Errorf("failed to get secret from provider %s: %w", providerName, err)
		}

		// Replace the secret reference with the actual value
		result = result[:start] + secretValue + result[end+1:]
	}

	return result, nil
}

func (l *FileLoader) getEnvFilePath(domain string) string {
	// Create domain-safe filename (replace dots and other chars with underscores)
	safeDomain := strings.ReplaceAll(domain, ".", "_")
	safeDomain = strings.ReplaceAll(safeDomain, ":", "_")
	safeDomain = strings.ReplaceAll(safeDomain, "/", "_")
	return filepath.Join(l.envDir, safeDomain+".env")
}
