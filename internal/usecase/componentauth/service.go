// Package componentauth implements component RPC token issuance and validation.
package componentauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/google/uuid"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const tokenPrefix = "gordon_component"

// Config controls component auth behavior.
type Config struct {
	Now func() time.Time
}

// CreateRequest describes a component token to create.
type CreateRequest struct {
	Name      string
	Role      domain.ComponentRole
	Scopes    []domain.ComponentScope
	ExpiresAt time.Time
}

// CreateResult returns the one-time plaintext token and safe metadata.
type CreateResult struct {
	Token    string
	Metadata domain.ComponentTokenMetadata
}

// Service issues and validates component tokens.
type Service struct {
	store out.ComponentTokenStore
	log   zerowrap.Logger
	now   func() time.Time
}

// NewService creates a component auth service.
func NewService(store out.ComponentTokenStore, log zerowrap.Logger, config Config) *Service {
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: store, log: log, now: now}
}

// CreateToken creates a component token, persists only its hash, and returns plaintext once.
func (s *Service) CreateToken(ctx context.Context, req CreateRequest) (*CreateResult, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("component name is required")
	}
	if req.Role == "" {
		return nil, fmt.Errorf("component role is required")
	}
	if !domain.IsKnownComponentRole(req.Role) {
		return nil, fmt.Errorf("unknown component role %q", req.Role)
	}

	scopes := append([]domain.ComponentScope(nil), req.Scopes...)
	if len(scopes) == 0 {
		scopes = domain.DefaultComponentScopesForRole(req.Role)
	}
	if err := validateScopesForRole(req.Role, scopes); err != nil {
		return nil, err
	}

	keyID := uuid.NewString()
	secret, err := randomSecret()
	if err != nil {
		return nil, fmt.Errorf("generate component token secret: %w", err)
	}
	token := formatToken(keyID, secret)
	now := s.now().UTC()
	record := &domain.ComponentTokenRecord{
		KeyID:     keyID,
		Prefix:    tokenPrefix,
		Name:      req.Name,
		Role:      req.Role,
		Scopes:    scopes,
		TokenHash: hashToken(token),
		CreatedAt: now,
		ExpiresAt: req.ExpiresAt,
	}

	if err := s.store.CreateComponentToken(ctx, record); err != nil {
		return nil, fmt.Errorf("create component token: %w", err)
	}

	s.log.Info().
		Str("key_id", keyID).
		Str("component", req.Name).
		Str("role", string(req.Role)).
		Msg("component token created")

	return &CreateResult{Token: token, Metadata: record.Metadata()}, nil
}

// ValidateToken validates token, required scope, revocation, and expiry and returns identity.
func (s *Service) ValidateToken(ctx context.Context, token string, required domain.ComponentScope) (*domain.ComponentIdentity, error) {
	prefix, keyID, err := parseToken(token)
	if err != nil {
		s.log.Warn().Msg("component token validation failed: invalid token format")
		return nil, domain.ErrInvalidToken
	}

	record, err := s.store.LookupComponentToken(ctx, prefix, keyID)
	if err != nil {
		return nil, fmt.Errorf("lookup component token: %w", err)
	}
	if record == nil {
		s.log.Warn().Str("key_id", keyID).Msg("component token validation failed: token not found")
		return nil, domain.ErrInvalidToken
	}
	if !constantTimeEqual(record.TokenHash, hashToken(token)) {
		s.log.Warn().Str("key_id", keyID).Msg("component token validation failed: hash mismatch")
		return nil, domain.ErrInvalidToken
	}
	if !record.RevokedAt.IsZero() {
		s.log.Warn().Str("key_id", keyID).Str("role", string(record.Role)).Msg("component token validation failed: token revoked")
		return nil, domain.ErrRevokedToken
	}
	if !record.ExpiresAt.IsZero() && !s.now().UTC().Before(record.ExpiresAt) {
		s.log.Warn().Str("key_id", keyID).Str("role", string(record.Role)).Msg("component token validation failed: token expired")
		return nil, domain.ErrExpiredToken
	}
	if required != "" && !domain.ComponentScopesContain(record.Scopes, required) {
		s.log.Warn().
			Str("key_id", keyID).
			Str("role", string(record.Role)).
			Str("required_scope", string(required)).
			Msg("component token validation failed: missing required scope")
		return nil, domain.ErrUnauthorized
	}

	now := s.now().UTC()
	if err := s.store.UpdateComponentTokenLastUsed(ctx, record.KeyID, now); err != nil {
		return nil, fmt.Errorf("update component token last used: %w", err)
	}

	return &domain.ComponentIdentity{
		KeyID:  record.KeyID,
		Name:   record.Name,
		Role:   record.Role,
		Scopes: append([]domain.ComponentScope(nil), record.Scopes...),
	}, nil
}

// RevokeToken revokes a component token by key ID.
func (s *Service) RevokeToken(ctx context.Context, keyID string) error {
	if keyID == "" {
		return fmt.Errorf("component token key ID is required")
	}
	if err := s.store.RevokeComponentToken(ctx, keyID, s.now().UTC()); err != nil {
		return fmt.Errorf("revoke component token: %w", err)
	}
	s.log.Info().Str("key_id", keyID).Msg("component token revoked")
	return nil
}

// ListTokenMetadata returns safe metadata without plaintext or hashes.
func (s *Service) ListTokenMetadata(ctx context.Context) ([]domain.ComponentTokenMetadata, error) {
	metadata, err := s.store.ListComponentTokenMetadata(ctx)
	if err != nil {
		return nil, fmt.Errorf("list component token metadata: %w", err)
	}
	return metadata, nil
}

func validateScopesForRole(role domain.ComponentRole, scopes []domain.ComponentScope) error {
	if len(scopes) == 0 {
		return fmt.Errorf("component role %q has no scopes", role)
	}
	for _, scope := range scopes {
		if !domain.IsKnownComponentScope(scope) {
			return fmt.Errorf("unknown component scope %q", scope)
		}
		if !domain.ComponentRoleAllowsScope(role, scope) {
			return fmt.Errorf("component scope %q is not allowed for role %q", scope, role)
		}
	}
	return nil
}

func formatToken(keyID, secret string) string {
	return tokenPrefix + "." + keyID + "." + secret
}

func parseToken(token string) (string, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != tokenPrefix || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("invalid component token format")
	}
	return parts[0], parts[1], nil
}

func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
