// Package tokenstore implements token storage adapters.
package tokenstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// ansiRegex matches ANSI escape sequences for stripping from pass output.
var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// subjectRegex validates token subjects to prevent path injection.
// Allows alphanumeric characters, forward slashes, underscores, dots, hyphens, and @.
// This prevents path traversal (../) and command injection attacks.
var subjectRegex = regexp.MustCompile(`^[a-zA-Z0-9/_.@-]+$`)

const (
	// passTokenPath is the base path for tokens in pass.
	passTokenPath = "gordon/registry/tokens" //nolint:gosec // Not a credential, this is a pass store path
	// passRevokedPath is the path for the revocation list in pass.
	passRevokedPath = "gordon/registry/revoked" //nolint:gosec // Not a credential, this is a pass store path
)

// cachedToken holds a token and its JWT in memory.
type cachedToken struct {
	jwt   string
	token *domain.Token
}

// validateSubject validates a token subject to prevent path injection and command injection.
// Returns an error if the subject is invalid.
func validateSubject(subject string) error {
	if subject == "" {
		return fmt.Errorf("subject cannot be empty")
	}

	if !subjectRegex.MatchString(subject) {
		return fmt.Errorf("invalid subject: must contain only alphanumeric characters, /, _, ., @, -")
	}

	if strings.Contains(subject, "..") {
		return fmt.Errorf("invalid subject: cannot contain '..' to prevent path traversal")
	}

	return nil
}

// PassStore implements TokenStore using the pass password manager.
type PassStore struct {
	timeout time.Duration
	log     zerowrap.Logger

	// In-memory cache to avoid repeated pass calls
	cacheMu     sync.RWMutex
	tokenCache  map[string]*cachedToken // keyed by subject
	revokedList []string                // cached revocation list
	revokedSet  map[string]struct{}     // for O(1) lookup
}

// NewPassStore creates a new pass-based token store.
func NewPassStore(log zerowrap.Logger) *PassStore {
	return &PassStore{
		timeout:    10 * time.Second,
		log:        log,
		tokenCache: make(map[string]*cachedToken),
		revokedSet: make(map[string]struct{}),
	}
}

// tokenMetadata holds the non-sensitive token information.
type tokenMetadata struct {
	ID             string    `json:"id"`
	Subject        string    `json:"subject"`
	Scopes         []string  `json:"scopes"`
	IssuedAt       time.Time `json:"issued_at"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	Revoked        bool      `json:"revoked"`
	LastExtendedAt time.Time `json:"last_extended_at"`
}

// SaveToken stores a token JWT and metadata in pass.
func (s *PassStore) SaveToken(ctx context.Context, token *domain.Token, jwt string) error {
	if err := validateSubject(token.Subject); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Store JWT
	jwtPath := fmt.Sprintf("%s/%s", passTokenPath, token.Subject)
	if err := s.passInsert(ctx, jwtPath, jwt); err != nil {
		return fmt.Errorf("failed to store token: %w", err)
	}

	// Store metadata
	meta := tokenMetadata{
		ID:             token.ID,
		Subject:        token.Subject,
		Scopes:         token.Scopes,
		IssuedAt:       token.IssuedAt,
		ExpiresAt:      token.ExpiresAt,
		Revoked:        token.Revoked,
		LastExtendedAt: token.LastExtendedAt,
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("failed to marshal token metadata: %w", err)
	}

	metaPath := fmt.Sprintf("%s/%s.meta", passTokenPath, token.Subject)
	if err := s.passInsert(ctx, metaPath, string(metaJSON)); err != nil {
		return fmt.Errorf("failed to store token metadata: %w", err)
	}

	// Update cache
	s.cacheMu.Lock()
	s.tokenCache[token.Subject] = &cachedToken{jwt: jwt, token: token}
	s.cacheMu.Unlock()

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
	if err := validateSubject(subject); err != nil {
		return "", nil, err
	}

	// Check cache first
	s.cacheMu.RLock()
	if cached, ok := s.tokenCache[subject]; ok {
		s.cacheMu.RUnlock()
		return cached.jwt, cached.token, nil
	}
	s.cacheMu.RUnlock()

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
		ID:             meta.ID,
		Subject:        meta.Subject,
		Scopes:         meta.Scopes,
		IssuedAt:       meta.IssuedAt,
		ExpiresAt:      meta.ExpiresAt,
		Revoked:        meta.Revoked,
		LastExtendedAt: meta.LastExtendedAt,
	}

	// Cache the token
	s.cacheMu.Lock()
	s.tokenCache[subject] = &cachedToken{jwt: jwt, token: token}
	s.cacheMu.Unlock()

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

		// Strip ANSI escape sequences (pass outputs colored text on TTY)
		line = ansiRegex.ReplaceAllString(line, "")

		// Skip tree formatting characters (both Unicode and ASCII variants)
		// Unicode: ├── └── │
		line = strings.TrimPrefix(line, "├── ")
		line = strings.TrimPrefix(line, "└── ")
		line = strings.TrimPrefix(line, "│   ")
		// ASCII: |-- `-- |
		line = strings.TrimPrefix(line, "|-- ")
		line = strings.TrimPrefix(line, "`-- ")
		line = strings.TrimPrefix(line, "|   ")

		line = strings.TrimSpace(line)

		// Skip .meta files, empty lines, and the header line
		if line == "" || strings.HasSuffix(line, ".meta") || line == passTokenPath {
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
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

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

	// Update cache
	s.revokedList = revokedList
	s.revokedSet = make(map[string]struct{}, len(revokedList))
	for _, id := range revokedList {
		s.revokedSet[id] = struct{}{}
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
	// Check cache first
	s.cacheMu.RLock()
	if s.revokedList != nil {
		_, revoked := s.revokedSet[tokenID]
		s.cacheMu.RUnlock()
		return revoked, nil
	}
	s.cacheMu.RUnlock()

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	revokedList, err := s.getRevokedList(ctx)
	if err != nil {
		return false, err
	}

	// Cache the revocation list
	s.cacheMu.Lock()
	s.revokedList = revokedList
	s.revokedSet = make(map[string]struct{}, len(revokedList))
	for _, id := range revokedList {
		s.revokedSet[id] = struct{}{}
	}
	_, revoked := s.revokedSet[tokenID]
	s.cacheMu.Unlock()

	return revoked, nil
}

// UpdateTokenExpiry updates the JWT and expiry metadata for an existing token.
// LastExtendedAt is also updated to track debounce timing.
func (s *PassStore) UpdateTokenExpiry(ctx context.Context, token *domain.Token, newJWT string) error {
	return s.SaveToken(ctx, token, newJWT)
}

// DeleteToken removes token from pass.
func (s *PassStore) DeleteToken(ctx context.Context, subject string) error {
	if err := validateSubject(subject); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	jwtPath := fmt.Sprintf("%s/%s", passTokenPath, subject)
	metaPath := fmt.Sprintf("%s/%s.meta", passTokenPath, subject)

	// Remove JWT
	// #nosec G204 -- command and args are fixed; subject is treated as data only.
	cmd := exec.CommandContext(ctx, "pass", "rm", "-f", jwtPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to delete token: %w", err)
	}

	// Remove metadata
	// #nosec G204 -- command and args are fixed; subject is treated as data only.
	cmd = exec.CommandContext(ctx, "pass", "rm", "-f", metaPath)
	if err := cmd.Run(); err != nil {
		s.log.Warn().Err(err).Msg("failed to delete token metadata")
	}

	// Clear from cache
	s.cacheMu.Lock()
	delete(s.tokenCache, subject)
	s.cacheMu.Unlock()

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
