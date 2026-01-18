// Package secrets implements the secret management use case.
package secrets

import (
	"context"
	"errors"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/out"
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
	store out.DomainSecretStore
	log   zerowrap.Logger
}

// NewService creates a new secrets service.
func NewService(store out.DomainSecretStore, log zerowrap.Logger) *Service {
	return &Service{
		store: store,
		log:   log,
	}
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

// ListKeysWithAttachments returns the list of secret keys for a domain
// along with any attachment secrets for containers associated with the domain.
func (s *Service) ListKeysWithAttachments(ctx context.Context, domain string) ([]string, []out.AttachmentSecrets, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListKeysWithAttachments",
		"domain":              domain,
	})
	log := zerowrap.FromCtx(ctx)

	if err := ValidateDomain(domain); err != nil {
		log.Warn().Err(err).Msg("domain validation failed")
		return nil, nil, err
	}

	// Get domain secrets
	keys, err := s.store.ListKeys(domain)
	if err != nil {
		log.Error().Err(err).Msg("failed to list secret keys")
		return nil, nil, err
	}

	// Get attachment secrets
	attachments, err := s.store.ListAttachmentKeys(domain)
	if err != nil {
		log.Warn().Err(err).Msg("failed to list attachment secrets, continuing without them")
		attachments = nil // Don't fail, just return empty attachments
	}

	log.Debug().
		Int("domain_keys", len(keys)).
		Int("attachments", len(attachments)).
		Msg("listed secret keys with attachments")

	return keys, attachments, nil
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

	log.Info().Msg("secret deleted")
	return nil
}

// ValidateDomain validates that a domain is safe to use for secret storage.
// This prevents path traversal attacks and ensures the domain is valid.
func ValidateDomain(domain string) error {
	// Check for empty domain
	if domain == "" {
		return ErrDomainEmpty
	}

	// Check domain length (max DNS name length is 253)
	if len(domain) > 253 {
		return ErrDomainTooLong
	}

	// Check for path traversal attempts
	if strings.Contains(domain, "..") {
		return ErrDomainPathTraversal
	}

	// Check for null bytes (can cause issues in file paths)
	if strings.ContainsRune(domain, '\x00') {
		return ErrDomainInvalidChars
	}

	return nil
}
