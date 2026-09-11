// Package secrets implements the secret management use case.
package secrets

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// Domain validation errors.
var (
	ErrDomainEmpty         = errors.New("domain cannot be empty")
	ErrDomainTooLong       = errors.New("domain exceeds maximum length of 253 characters")
	ErrDomainPathTraversal = errors.New("domain contains path traversal sequence")
	ErrDomainInvalidChars  = errors.New("domain contains invalid characters")
)

// Service implements the SecretService interface.
type Service struct {
	store    out.DomainSecretStore
	log      zerowrap.Logger
	eventBus out.EventPublisher
}

// NewService creates a new secrets service.
func NewService(store out.DomainSecretStore, log zerowrap.Logger, eventBus out.EventPublisher) *Service {
	return &Service{
		store:    store,
		log:      log,
		eventBus: eventBus,
	}
}

// publishSecretsChanged publishes a secrets.changed event.
// Failures are logged but do not fail the mutation - secret storage
// is the source of truth; the event is a best-effort notification.
func (s *Service) publishSecretsChanged(ctx context.Context, domainName, operation string, keys []string) {
	if s.eventBus == nil {
		return
	}
	log := zerowrap.FromCtx(ctx)
	payload := domain.SecretsChangedPayload{
		Domain:    domainName,
		Operation: operation,
		Keys:      keys,
	}
	if err := s.eventBus.Publish(domain.EventSecretsChanged, payload); err != nil {
		log.Warn().Err(err).Str("domain", domainName).Msg("failed to publish secrets.changed event")
	}
}

// sortedKeys returns the keys of a map in sorted order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ListKeys returns the list of secret keys for a domain (not values).
func (s *Service) ListKeys(ctx context.Context, domain string) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListKeys",
		"domain":              domain,
	})
	log := zerowrap.FromCtx(ctx)

	if err := ValidateDomain(domain); err != nil {
		log.Warn().Err(err).Msg("domain validation failed")
		return nil, err
	}

	keys, err := s.store.ListKeys(domain)
	if err != nil {
		log.Error().Err(err).Msg("failed to list secret keys")
		return nil, err
	}

	log.Debug().Int("count", len(keys)).Msg("listed secret keys")
	return keys, nil
}

// GetAll returns all secrets for a domain as a key-value map.
func (s *Service) GetAll(ctx context.Context, domain string) (map[string]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetAll",
		"domain":              domain,
	})
	log := zerowrap.FromCtx(ctx)

	if err := ValidateDomain(domain); err != nil {
		log.Warn().Err(err).Msg("domain validation failed")
		return nil, err
	}

	secrets, err := s.store.GetAll(domain)
	if err != nil {
		log.Error().Err(err).Msg("failed to get secrets")
		return nil, err
	}

	log.Debug().Int("count", len(secrets)).Msg("retrieved secrets")
	return secrets, nil
}

// Set sets or updates multiple secrets for a domain, merging with existing.
func (s *Service) Set(ctx context.Context, domain string, secrets map[string]string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Set",
		"domain":              domain,
	})
	log := zerowrap.FromCtx(ctx)

	if err := ValidateDomain(domain); err != nil {
		log.Warn().Err(err).Msg("domain validation failed")
		return err
	}

	if err := s.store.Set(domain, secrets); err != nil {
		log.Error().Err(err).Msg("failed to set secrets")
		return err
	}

	s.publishSecretsChanged(ctx, domain, "set", sortedKeys(secrets))

	log.Info().Int("count", len(secrets)).Msg("secrets set")
	return nil
}

// Delete removes a specific secret key from a domain.
func (s *Service) Delete(ctx context.Context, domain, key string) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Delete",
		"domain":              domain,
		"key":                 key,
	})
	log := zerowrap.FromCtx(ctx)

	if err := ValidateDomain(domain); err != nil {
		log.Warn().Err(err).Msg("domain validation failed")
		return err
	}

	if err := s.store.Delete(domain, key); err != nil {
		log.Error().Err(err).Msg("failed to delete secret")
		return err
	}

	s.publishSecretsChanged(ctx, domain, "delete", []string{key})

	log.Info().Msg("secret deleted")
	return nil
}

// ValidateDomain validates that a domain is safe to use for secret storage.
// This prevents path traversal attacks and ensures the domain is valid.
func ValidateDomain(domainName string) error {
	// Check for empty domain
	if domainName == "" {
		return ErrDomainEmpty
	}

	// Check domain length (max DNS name length is 253)
	if len(domainName) > 253 {
		return ErrDomainTooLong
	}

	// Check for path traversal attempts
	if strings.Contains(domainName, "..") {
		return ErrDomainPathTraversal
	}

	// Check for null bytes (can cause issues in file paths)
	if strings.ContainsRune(domainName, '\x00') {
		return ErrDomainInvalidChars
	}

	// Reuse the env-file sanitizer so validation matches the on-disk naming
	// scheme and rejects ambiguous names that would collide after sanitization.
	if _, err := domain.SanitizeDomainForEnvFile(domainName); err != nil {
		return ErrDomainInvalidChars
	}

	return nil
}
