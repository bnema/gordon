package tokenstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bnema/zerowrap"

	"gordon/internal/domain"
)

const (
	// unsafeTokenDir is the subdirectory for tokens in the data directory.
	unsafeTokenDir = "secrets/gordon/registry/tokens"
	// unsafeRevokedFile is the filename for the revocation list.
	unsafeRevokedFile = "secrets/gordon/registry/revoked.json"
)

// UnsafeStore implements TokenStore using plain text files.
// WARNING: This store does not encrypt secrets. Only use when pass/sops are unavailable.
type UnsafeStore struct {
	dataDir string
	log     zerowrap.Logger
}

// NewUnsafeStore creates a new file-based token store.
// dataDir is the base directory for storing secrets (typically gordon's data_dir).
func NewUnsafeStore(dataDir string, log zerowrap.Logger) *UnsafeStore {
	store := &UnsafeStore{
		dataDir: dataDir,
		log:     log,
	}

	log.Warn().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "tokenstore").
		Str("provider", "unsafe").
		Str("data_dir", dataDir).
		Msg("using unsafe secrets backend - secrets are stored in plain text")

	return store
}

// unsafeTokenData holds both JWT and metadata in a single file.
type unsafeTokenData struct {
	JWT      string        `json:"jwt"`
	Metadata tokenMetadata `json:"metadata"`
}

// SaveToken stores a token JWT and metadata as a JSON file.
func (s *UnsafeStore) SaveToken(_ context.Context, token *domain.Token, jwt string) error {
	tokenDir := filepath.Join(s.dataDir, unsafeTokenDir)
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		return fmt.Errorf("failed to create token directory: %w", err)
	}

	data := unsafeTokenData{
		JWT: jwt,
		Metadata: tokenMetadata{
			ID:        token.ID,
			Subject:   token.Subject,
			Scopes:    token.Scopes,
			IssuedAt:  token.IssuedAt,
			ExpiresAt: token.ExpiresAt,
			Revoked:   token.Revoked,
		},
	}

	dataJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token data: %w", err)
	}

	tokenFile := filepath.Join(tokenDir, token.Subject+".json")
	if err := os.WriteFile(tokenFile, dataJSON, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %w", err)
	}

	s.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "tokenstore").
		Str("provider", "unsafe").
		Str("subject", token.Subject).
		Msg("token stored in file")

	return nil
}

// GetToken retrieves token JWT by subject from file.
func (s *UnsafeStore) GetToken(_ context.Context, subject string) (string, *domain.Token, error) {
	tokenFile := filepath.Join(s.dataDir, unsafeTokenDir, subject+".json")

	dataJSON, err := os.ReadFile(tokenFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, domain.ErrTokenNotFound
		}
		return "", nil, fmt.Errorf("failed to read token file: %w", err)
	}

	var data unsafeTokenData
	if err := json.Unmarshal(dataJSON, &data); err != nil {
		return "", nil, fmt.Errorf("failed to unmarshal token data: %w", err)
	}

	token := &domain.Token{
		ID:        data.Metadata.ID,
		Subject:   data.Metadata.Subject,
		Scopes:    data.Metadata.Scopes,
		IssuedAt:  data.Metadata.IssuedAt,
		ExpiresAt: data.Metadata.ExpiresAt,
		Revoked:   data.Metadata.Revoked,
	}

	return data.JWT, token, nil
}

// ListTokens returns all stored tokens from files.
func (s *UnsafeStore) ListTokens(ctx context.Context) ([]domain.Token, error) {
	tokenDir := filepath.Join(s.dataDir, unsafeTokenDir)

	entries, err := os.ReadDir(tokenDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.Token{}, nil
		}
		return nil, fmt.Errorf("failed to read token directory: %w", err)
	}

	var tokens []domain.Token
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		subject := strings.TrimSuffix(entry.Name(), ".json")
		_, token, err := s.GetToken(ctx, subject)
		if err != nil {
			s.log.Warn().Err(err).Str("subject", subject).Msg("failed to get token")
			continue
		}

		tokens = append(tokens, *token)
	}

	return tokens, nil
}

// Revoke adds token ID to revocation list file.
func (s *UnsafeStore) Revoke(_ context.Context, tokenID string) error {
	revokedFile := filepath.Join(s.dataDir, unsafeRevokedFile)

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(revokedFile), 0700); err != nil {
		return fmt.Errorf("failed to create revoked directory: %w", err)
	}

	// Get current list
	revokedList, err := s.getRevokedList()
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
	listJSON, err := json.MarshalIndent(revokedList, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal revocation list: %w", err)
	}

	if err := os.WriteFile(revokedFile, listJSON, 0600); err != nil {
		return fmt.Errorf("failed to write revocation list: %w", err)
	}

	s.log.Info().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "tokenstore").
		Str("token_id", tokenID).
		Msg("token revoked")

	return nil
}

// IsRevoked checks if token ID is in revocation list.
func (s *UnsafeStore) IsRevoked(_ context.Context, tokenID string) (bool, error) {
	revokedList, err := s.getRevokedList()
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

// DeleteToken removes token file.
func (s *UnsafeStore) DeleteToken(_ context.Context, subject string) error {
	tokenFile := filepath.Join(s.dataDir, unsafeTokenDir, subject+".json")

	if err := os.Remove(tokenFile); err != nil {
		if os.IsNotExist(err) {
			return domain.ErrTokenNotFound
		}
		return fmt.Errorf("failed to delete token file: %w", err)
	}

	s.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "tokenstore").
		Str("subject", subject).
		Msg("token deleted from file")

	return nil
}

// getRevokedList retrieves the current revocation list from file.
func (s *UnsafeStore) getRevokedList() ([]string, error) {
	revokedFile := filepath.Join(s.dataDir, unsafeRevokedFile)

	listJSON, err := os.ReadFile(revokedFile)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to read revocation list: %w", err)
	}

	var revokedList []string
	if err := json.Unmarshal(listJSON, &revokedList); err != nil {
		return nil, fmt.Errorf("failed to unmarshal revocation list: %w", err)
	}

	return revokedList, nil
}

// IsAvailable always returns true for file-based storage.
func (s *UnsafeStore) IsAvailable() bool {
	return true
}
