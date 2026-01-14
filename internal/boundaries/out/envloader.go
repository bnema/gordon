package out

import "context"

// EnvLoader defines the contract for loading environment variables for containers.
type EnvLoader interface {
	// LoadEnv loads environment variables for a given domain.
	// Returns a slice of "KEY=VALUE" strings.
	LoadEnv(ctx context.Context, domain string) ([]string, error)

	// CreateEnvFile creates an empty environment file for a new domain.
	CreateEnvFile(ctx context.Context, domain string) error

	// EnvFileExists checks if an environment file exists for a domain.
	EnvFileExists(domain string) bool
}
