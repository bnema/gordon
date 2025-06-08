package env

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gordon/internal/config"

	"github.com/rs/zerolog/log"
)

type Loader struct {
	cfg             *config.Config
	secretProviders map[string]SecretProvider
}

func NewLoader(cfg *config.Config) *Loader {
	return &Loader{
		cfg:             cfg,
		secretProviders: make(map[string]SecretProvider),
	}
}

func (l *Loader) RegisterSecretProvider(name string, provider SecretProvider) {
	l.secretProviders[name] = provider
}

func (l *Loader) LoadEnvForRoute(domain string) ([]string, error) {
	envVars := []string{}

	// Create domain-safe filename (replace dots and other chars with underscores)
	safeDomain := strings.ReplaceAll(domain, ".", "_")
	safeDomain = strings.ReplaceAll(safeDomain, ":", "_")
	safeDomain = strings.ReplaceAll(safeDomain, "/", "_")

	envFile := filepath.Join(l.cfg.Env.Dir, safeDomain+".env")

	// Check if env file exists
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		log.Debug().
			Str("domain", domain).
			Str("env_file", envFile).
			Msg("No env file found for route")
		return envVars, nil
	}

	log.Debug().
		Str("domain", domain).
		Str("env_file", envFile).
		Msg("Loading env file for route")

	file, err := os.Open(envFile)
	if err != nil {
		return nil, fmt.Errorf("failed to open env file %s: %w", envFile, err)
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
				Str("file", envFile).
				Int("line", lineNum).
				Str("content", line).
				Msg("Invalid env file line format, skipping")
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
		resolvedValue, err := l.resolveSecrets(value)
		if err != nil {
			log.Error().
				Err(err).
				Str("key", key).
				Str("file", envFile).
				Int("line", lineNum).
				Msg("Failed to resolve secret")
			return nil, fmt.Errorf("failed to resolve secret for %s in %s line %d: %w", key, envFile, lineNum, err)
		}

		envVars = append(envVars, fmt.Sprintf("%s=%s", key, resolvedValue))
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading env file %s: %w", envFile, err)
	}

	log.Info().
		Str("domain", domain).
		Int("count", len(envVars)).
		Msg("Loaded environment variables for route")

	return envVars, nil
}

func (l *Loader) resolveSecrets(value string) (string, error) {
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

		secretValue, err := provider.GetSecret(secretPath)
		if err != nil {
			return "", fmt.Errorf("failed to get secret from provider %s: %w", providerName, err)
		}

		// Replace the secret reference with the actual value
		result = result[:start] + secretValue + result[end+1:]
	}

	return result, nil
}

func (l *Loader) EnsureEnvDir() error {
	if err := os.MkdirAll(l.cfg.Env.Dir, 0700); err != nil {
		return fmt.Errorf("failed to create env directory %s: %w", l.cfg.Env.Dir, err)
	}

	log.Debug().
		Str("env_dir", l.cfg.Env.Dir).
		Msg("Ensured env directory exists")

	return nil
}

func (l *Loader) CreateEnvFilesForRoutes() error {
	routes := l.cfg.GetRoutes()
	if len(routes) == 0 {
		log.Debug().Msg("No routes configured, skipping env file creation")
		return nil
	}

	createdCount := 0
	for _, route := range routes {
		// Create domain-safe filename (same logic as LoadEnvForRoute)
		safeDomain := strings.ReplaceAll(route.Domain, ".", "_")
		safeDomain = strings.ReplaceAll(safeDomain, ":", "_")
		safeDomain = strings.ReplaceAll(safeDomain, "/", "_")

		envFile := filepath.Join(l.cfg.Env.Dir, safeDomain+".env")

		// Check if file already exists
		if _, err := os.Stat(envFile); err == nil {
			log.Debug().
				Str("domain", route.Domain).
				Str("env_file", envFile).
				Msg("Env file already exists, skipping creation")
			continue
		} else if !os.IsNotExist(err) {
			log.Warn().
				Err(err).
				Str("env_file", envFile).
				Msg("Error checking env file, skipping creation")
			continue
		}

		// Create empty env file with helpful comments
		content := fmt.Sprintf(`# Environment variables for route: %s
# Image: %s

`, route.Domain, route.Image)

		if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
			log.Error().
				Err(err).
				Str("domain", route.Domain).
				Str("env_file", envFile).
				Msg("Failed to create env file")
			continue
		}

		createdCount++
		log.Info().
			Str("domain", route.Domain).
			Str("env_file", envFile).
			Msg("Created empty env file for route")
	}

	if createdCount > 0 {
		log.Info().
			Int("created", createdCount).
			Int("total_routes", len(routes)).
			Str("env_dir", l.cfg.Env.Dir).
			Msg("Created env files for routes")
	}

	return nil
}

func (l *Loader) CreateEnvFileForRoute(domain, image string) error {
	// Create domain-safe filename (same logic as LoadEnvForRoute)
	safeDomain := strings.ReplaceAll(domain, ".", "_")
	safeDomain = strings.ReplaceAll(safeDomain, ":", "_")
	safeDomain = strings.ReplaceAll(safeDomain, "/", "_")

	envFile := filepath.Join(l.cfg.Env.Dir, safeDomain+".env")

	// Check if file already exists
	if _, err := os.Stat(envFile); err == nil {
		log.Debug().
			Str("domain", domain).
			Str("env_file", envFile).
			Msg("Env file already exists, skipping creation")
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("error checking env file %s: %w", envFile, err)
	}

	// Ensure directory exists
	if err := l.EnsureEnvDir(); err != nil {
		return err
	}

	// Create empty env file with helpful comments
	content := fmt.Sprintf(`# Environment variables for route: %s
# Image: %s

`, domain, image)

	if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
		return fmt.Errorf("failed to create env file %s: %w", envFile, err)
	}

	log.Info().
		Str("domain", domain).
		Str("env_file", envFile).
		Msg("Created empty env file for new route")

	return nil
}

func (l *Loader) UpdateConfig(cfg *config.Config) {
	l.cfg = cfg
}

func (l *Loader) GetEnvFilePath(domain string) string {
	safeDomain := strings.ReplaceAll(domain, ".", "_")
	safeDomain = strings.ReplaceAll(safeDomain, ":", "_")
	safeDomain = strings.ReplaceAll(safeDomain, "/", "_")
	return filepath.Join(l.cfg.Env.Dir, safeDomain+".env")
}