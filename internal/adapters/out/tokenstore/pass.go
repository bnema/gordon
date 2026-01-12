// Package tokenstore implements token storage adapters.
package tokenstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"gordon/internal/domain"
)

const (
	// passTokenPath is the base path for tokens in pass.
	passTokenPath = "gordon/registry/tokens"
	// passRevokedPath is the path for the revocation list in pass.
	passRevokedPath = "gordon/registry/revoked"
)

// PassStore implements TokenStore using the pass password manager.
type PassStore struct {
	timeout time.Duration
	log     zerowrap.Logger
}

// NewPassStore creates a new pass-based token store.
func NewPassStore(log zerowrap.Logger) *PassStore {
	return &PassStore{
		timeout: 10 * time.Second,
		log:     log,
	}
}

// tokenMetadata holds the non-sensitive token information.
type tokenMetadata struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	Scopes    []string  `json:"scopes"`
	IssuedAt  time.Time `json:"issued_at"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	Revoked   bool      `json:"revoked"`
}

// SaveToken stores a token JWT and metadata in pass.
func (s *PassStore) SaveToken(ctx context.Context, token *domain.Token, jwt string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Store JWT
	jwtPath := fmt.Sprintf("%s/%s", passTokenPath, token.Subject)
	if err := s.passInsert(ctx, jwtPath, jwt); err != nil {
		return fmt.Errorf("failed to store token: %w", err)
	}

	// Store metadata
	meta := tokenMetadata{
		ID:        token.ID,
		Subject:   token.Subject,
		Scopes:    token.Scopes,
		IssuedAt:  token.IssuedAt,
		ExpiresAt: token.ExpiresAt,
		Revoked:   token.Revoked,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal token metadata: %w", err)
	}

	metaPath := fmt.Sprintf("%s/%s.meta", passTokenPath, token.Subject)
	if err := s.passInsert(ctx, metaPath, string(metaJSON)); err != nil {
		return fmt.Errorf("failed to store token metadata: %w", err)
	}

	s.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "tokenstore").
		Str("provider", "pass").
		Str("subject", token.Subject).
		Msg("token stored in pass")

	return nil
}

// GetToken retrieves token JWT by subject from pass.
func (s *PassStore) GetToken(ctx context.Context, subject string) (string, *domain.Token, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Get JWT
	jwtPath := fmt.Sprintf("%s/%s", passTokenPath, subject)
	jwt, err := s.passShow(ctx, jwtPath)
	if err != nil {
		return "", nil, domain.ErrTokenNotFound
	}

	// Get metadata
	metaPath := fmt.Sprintf("%s/%s.meta", passTokenPath, subject)
	metaJSON, err := s.passShow(ctx, metaPath)
	if err != nil {
		return "", nil, fmt.Errorf("failed to get token metadata: %w", err)
	}

	var meta tokenMetadata
	if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
		return "", nil, fmt.Errorf("failed to unmarshal token metadata: %w", err)
	}

	token := &domain.Token{
		ID:        meta.ID,
		Subject:   meta.Subject,
		Scopes:    meta.Scopes,
		IssuedAt:  meta.IssuedAt,
		ExpiresAt: meta.ExpiresAt,
		Revoked:   meta.Revoked,
	}

	return jwt, token, nil
}

// ListTokens returns all stored tokens from pass.
func (s *PassStore) ListTokens(ctx context.Context) ([]domain.Token, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// List all entries under the token path
	cmd := exec.CommandContext(ctx, "pass", "ls", passTokenPath)
	output, err := cmd.Output()
	if err != nil {
		// If the path doesn't exist, return empty list
		return []domain.Token{}, nil
	}

	var tokens []domain.Token
	lines := strings.Split(string(output), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip tree formatting characters and empty lines
		line = strings.TrimPrefix(line, "├── ")
		line = strings.TrimPrefix(line, "└── ")
		line = strings.TrimPrefix(line, "│   ")

		// Skip .meta files and empty lines
		if line == "" || strings.HasSuffix(line, ".meta") {
			continue
		}

		// Try to get the token metadata
		_, token, err := s.GetToken(ctx, line)
		if err != nil {
			s.log.Warn().Err(err).Str("subject", line).Msg("failed to get token")
			continue
		}

		tokens = append(tokens, *token)
	}

	return tokens, nil
}

// Revoke adds token ID to revocation list in pass.
func (s *PassStore) Revoke(ctx context.Context, tokenID string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Get current revocation list
	revokedList, err := s.getRevokedList(ctx)
	if err != nil {
		return err
	}

	// Add to list if not already present
	for _, id := range revokedList {
		if id == tokenID {
			return nil // Already revoked
		}
	}
	revokedList = append(revokedList, tokenID)

	// Store updated list
	listJSON, err := json.Marshal(revokedList)
	if err != nil {
		return fmt.Errorf("failed to marshal revocation list: %w", err)
	}

	if err := s.passInsert(ctx, passRevokedPath, string(listJSON)); err != nil {
		return fmt.Errorf("failed to store revocation list: %w", err)
	}

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "tokenstore").
		Str("token_id", tokenID).
		Msg("token revoked")

	return nil
}

// IsRevoked checks if token ID is in revocation list.
func (s *PassStore) IsRevoked(ctx context.Context, tokenID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	revokedList, err := s.getRevokedList(ctx)
	if err != nil {
		return false, err
	}

	for _, id := range revokedList {
		if id == tokenID {
			return true, nil
		}
	}

	return false, nil
}

// DeleteToken removes token from pass.
func (s *PassStore) DeleteToken(ctx context.Context, subject string) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	jwtPath := fmt.Sprintf("%s/%s", passTokenPath, subject)
	metaPath := fmt.Sprintf("%s/%s.meta", passTokenPath, subject)

	// Remove JWT
	cmd := exec.CommandContext(ctx, "pass", "rm", "-f", jwtPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete token: %w", err)
	}

	// Remove metadata
	cmd = exec.CommandContext(ctx, "pass", "rm", "-f", metaPath)
	if err := cmd.Run(); err != nil {
		s.log.Warn().Err(err).Msg("failed to delete token metadata")
	}

	s.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "tokenstore").
		Str("subject", subject).
		Msg("token deleted from pass")

	return nil
}

// passInsert inserts a value into pass.
func (s *PassStore) passInsert(ctx context.Context, path, value string) error {
	cmd := exec.CommandContext(ctx, "pass", "insert", "-m", "-f", path)
	cmd.Stdin = strings.NewReader(value)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pass insert failed: %s: %w", string(output), err)
	}
	return nil
}

// passShow retrieves a value from pass.
func (s *PassStore) passShow(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "pass", "show", path)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pass show failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// getRevokedList retrieves the current revocation list.
func (s *PassStore) getRevokedList(ctx context.Context) ([]string, error) {
	listJSON, err := s.passShow(ctx, passRevokedPath)
	if err != nil {
		// If the list doesn't exist, return empty list
		return []string{}, nil
	}

	var revokedList []string
	if err := json.Unmarshal([]byte(listJSON), &revokedList); err != nil {
		return nil, fmt.Errorf("failed to unmarshal revocation list: %w", err)
	}

	return revokedList, nil
}

// IsAvailable checks if pass is available in the system.
func (s *PassStore) IsAvailable() bool {
	cmd := exec.Command("pass", "version")
	return cmd.Run() == nil
}
