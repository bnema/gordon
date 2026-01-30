// Package envloader implements the environment variable loader adapter.
package envloader

import (
	"context"
	"fmt"
	"sort"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
)

// PassLoader implements the EnvLoader interface using pass-backed secrets.
type PassLoader struct {
	store *domainsecrets.PassStore
	log   zerowrap.Logger
}

// NewPassLoader creates a new pass-based environment loader.
func NewPassLoader(store *domainsecrets.PassStore, log zerowrap.Logger) (*PassLoader, error) {
	if store == nil {
		return nil, fmt.Errorf("pass store is required")
	}

	log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "envloader").
		Str("provider", "pass").
		Msg("env loader initialized")

	return &PassLoader{
		store: store,
		log:   log,
	}, nil
}

// LoadEnv loads environment variables for a given domain.
func (l *PassLoader) LoadEnv(ctx context.Context, domain string) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "envloader",
		zerowrap.FieldAction:  "LoadEnv",
		"domain":              domain,
	})
	log := zerowrap.FromCtx(ctx)

	secretsMap, err := l.store.GetAll(domain)
	if err != nil {
		return nil, log.WrapErr(err, "failed to load env from pass")
	}

	keys := make([]string, 0, len(secretsMap))
	for key := range secretsMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	envVars := make([]string, 0, len(keys))
	for _, key := range keys {
		envVars = append(envVars, fmt.Sprintf("%s=%s", key, secretsMap[key]))
	}

	log.Info().Int(zerowrap.FieldCount, len(envVars)).Msg("loaded environment variables for route")

	return envVars, nil
}

// CreateEnvFile is a no-op for pass-backed loader.
func (l *PassLoader) CreateEnvFile(ctx context.Context, domain string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "adapter",
		zerowrap.FieldAdapter: "envloader",
		zerowrap.FieldAction:  "CreateEnvFile",
		"domain":              domain,
	})
	log := zerowrap.FromCtx(ctx)
	log.Debug().Msg("pass loader does not create env files")
	return nil
}

// EnvFileExists checks if a manifest exists for a domain in pass.
func (l *PassLoader) EnvFileExists(domain string) bool {
	exists, err := l.store.ManifestExists(domain)
	if err != nil {
		l.log.Warn().Err(err).Str("domain", domain).Msg("failed to check pass manifest")
		return false
	}
	return exists
}
