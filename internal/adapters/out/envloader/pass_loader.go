// Package envloader implements the environment variable loader adapter.
package envloader

import (
	"context"
	"fmt"
	"sort"
	"strings"

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

	var secretsMap map[string]string
	var err error

	if isAttachmentContainer(domain) {
		secretsMap, err = l.store.GetAllAttachment(domain)
		if err != nil {
			return nil, log.WrapErr(err, "failed to load attachment env from pass")
		}
	} else {
		secretsMap, err = l.store.GetAll(domain)
		if err != nil {
			return nil, log.WrapErr(err, "failed to load env from pass")
		}
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

// isAttachmentContainer determines whether a given name refers to an attachment container
// or a domain. It uses a simple heuristic: attachment container names always start with
// the "gordon-" prefix (e.g., "gordon-git-example-com-gitea-postgres").
//
// LIMITATION: This heuristic assumes domains never start with "gordon-". A domain like
// "gordon-app.example.com" would be incorrectly classified as an attachment container.
// This is an acceptable trade-off for simplicity, but if such domains become common,
// this should be replaced with a more robust detection mechanism (e.g., checking both
// GetAll() and GetAllAttachment() and seeing which returns results).
func isAttachmentContainer(domain string) bool {
	return strings.HasPrefix(domain, "gordon-")
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
