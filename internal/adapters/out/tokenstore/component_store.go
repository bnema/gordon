package tokenstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const (
	unsafeComponentTokenDir           = "secrets/gordon/component-tokens"
	unsafeComponentTokenRevocationDir = "secrets/gordon/component-token-revocations"
	passComponentTokenPath            = "gordon/component-tokens"            //nolint:gosec // This is a pass store path, not a credential.
	passComponentTokenRevocationPath  = "gordon/component-token-revocations" //nolint:gosec // This is a pass store path, not a credential.
)

// componentTokenData intentionally contains a hash and safe metadata only.
// Plaintext component tokens must never be added to this type.
type componentTokenData struct {
	KeyID     string                  `json:"key_id"`
	Prefix    string                  `json:"prefix"`
	Name      string                  `json:"name"`
	Role      domain.ComponentRole    `json:"role"`
	Scopes    []domain.ComponentScope `json:"scopes"`
	TokenHash string                  `json:"token_hash"`
	CreatedAt time.Time               `json:"created_at"`
	ExpiresAt time.Time               `json:"expires_at,omitempty"`
	// RevokedAt is retained only to read records written before revocations were
	// made monotonic. New revocations are stored in a separate marker.
	RevokedAt  time.Time `json:"revoked_at,omitempty"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
}

// componentTokenRevocation is a separate, authoritative record. Keeping it
// apart from mutable token metadata means a stale last-used write can never
// restore a revoked token.
type componentTokenRevocation struct {
	KeyID     string    `json:"key_id"`
	RevokedAt time.Time `json:"revoked_at"`
}

// NewComponentTokenStore creates a ComponentTokenStore using the configured secrets backend.
func NewComponentTokenStore(backend domain.SecretsBackend, dataDir string, log zerowrap.Logger) (out.ComponentTokenStore, error) {
	switch backend {
	case domain.SecretsBackendPass:
		store := NewPassStore(log)
		if !store.IsAvailable() {
			return nil, fmt.Errorf("pass is not available in the system")
		}
		return store, nil
	case domain.SecretsBackendUnsafe:
		if dataDir == "" {
			return nil, fmt.Errorf("data_dir is required for unsafe backend")
		}
		return NewUnsafeStore(dataDir, log)
	case domain.SecretsBackendSops:
		return nil, fmt.Errorf("sops backend is not yet implemented")
	default:
		return nil, fmt.Errorf("unknown secrets backend: %s", backend)
	}
}

func componentTokenFileName(keyID string) string {
	hash := sha256.Sum256([]byte(keyID))
	return hex.EncodeToString(hash[:]) + ".json"
}

func componentTokenPassPath(keyID string) string {
	return passComponentTokenPath + "/" + strings.TrimSuffix(componentTokenFileName(keyID), ".json")
}

func componentTokenRevocationPassPath(keyID string) string {
	return passComponentTokenRevocationPath + "/" + strings.TrimSuffix(componentTokenFileName(keyID), ".json")
}

func marshalComponentTokenRevocation(keyID string, revokedAt time.Time) ([]byte, error) {
	if keyID == "" {
		return nil, fmt.Errorf("component token key ID is required")
	}
	if revokedAt.IsZero() {
		return nil, fmt.Errorf("component token revocation time is required")
	}
	payload, err := json.Marshal(componentTokenRevocation{KeyID: keyID, RevokedAt: revokedAt})
	if err != nil {
		return nil, fmt.Errorf("marshal component token revocation: %w", err)
	}
	return payload, nil
}

func unmarshalComponentTokenRevocation(payload []byte, keyID string) (time.Time, error) {
	var revocation componentTokenRevocation
	if err := json.Unmarshal(payload, &revocation); err != nil {
		return time.Time{}, fmt.Errorf("unmarshal component token revocation: %w", err)
	}
	if revocation.KeyID != keyID || revocation.RevokedAt.IsZero() {
		return time.Time{}, fmt.Errorf("component token revocation does not match its path")
	}
	return revocation.RevokedAt, nil
}

func componentTokenDataFromRecord(record *domain.ComponentTokenRecord) (componentTokenData, error) {
	if record == nil {
		return componentTokenData{}, fmt.Errorf("component token record is required")
	}
	if record.KeyID == "" {
		return componentTokenData{}, fmt.Errorf("component token key ID is required")
	}
	if len(record.TokenHash) != sha256.Size*2 {
		return componentTokenData{}, fmt.Errorf("component token hash must be a SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(record.TokenHash); err != nil {
		return componentTokenData{}, fmt.Errorf("component token hash must be a SHA-256 hex digest: %w", err)
	}
	return componentTokenData{
		KeyID:      record.KeyID,
		Prefix:     record.Prefix,
		Name:       record.Name,
		Role:       record.Role,
		Scopes:     append([]domain.ComponentScope(nil), record.Scopes...),
		TokenHash:  record.TokenHash,
		CreatedAt:  record.CreatedAt,
		ExpiresAt:  record.ExpiresAt,
		RevokedAt:  record.RevokedAt,
		LastUsedAt: record.LastUsedAt,
	}, nil
}

func (data componentTokenData) record() *domain.ComponentTokenRecord {
	return &domain.ComponentTokenRecord{
		KeyID:      data.KeyID,
		Prefix:     data.Prefix,
		Name:       data.Name,
		Role:       data.Role,
		Scopes:     append([]domain.ComponentScope(nil), data.Scopes...),
		TokenHash:  data.TokenHash,
		CreatedAt:  data.CreatedAt,
		ExpiresAt:  data.ExpiresAt,
		RevokedAt:  data.RevokedAt,
		LastUsedAt: data.LastUsedAt,
	}
}

func marshalComponentTokenRecord(record *domain.ComponentTokenRecord) ([]byte, error) {
	data, err := componentTokenDataFromRecord(record)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("marshal component token record: %w", err)
	}
	return payload, nil
}

func unmarshalComponentTokenRecord(payload []byte, keyID string) (*domain.ComponentTokenRecord, error) {
	var data componentTokenData
	if err := json.Unmarshal(payload, &data); err != nil {
		return nil, fmt.Errorf("unmarshal component token record: %w", err)
	}
	if data.KeyID != keyID {
		return nil, fmt.Errorf("component token record key ID does not match its path")
	}
	return data.record(), nil
}

func (s *UnsafeStore) componentTokenPath(keyID string) string {
	return filepath.Join(s.dataDir, unsafeComponentTokenDir, componentTokenFileName(keyID))
}

func (s *UnsafeStore) componentTokenRevocationPath(keyID string) string {
	return filepath.Join(s.dataDir, unsafeComponentTokenRevocationDir, componentTokenFileName(keyID))
}

func (s *UnsafeStore) ensureComponentTokenDir() (string, error) {
	return s.ensureComponentTokenSubdir(unsafeComponentTokenDir)
}

func (s *UnsafeStore) ensureComponentTokenRevocationDir() (string, error) {
	return s.ensureComponentTokenSubdir(unsafeComponentTokenRevocationDir)
}

func (s *UnsafeStore) ensureComponentTokenSubdir(subdir string) (string, error) {
	dir := filepath.Join(s.dataDir, subdir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create component token directory: %w", err)
	}
	for _, path := range []string{
		filepath.Join(s.dataDir, "secrets"),
		filepath.Join(s.dataDir, "secrets", "gordon"),
		dir,
	} {
		//nolint:gosec // Component-token directories must be accessible only to their owner.
		if err := os.Chmod(path, 0700); err != nil {
			return "", fmt.Errorf("restrict component token directory permissions: %w", err)
		}
	}
	return dir, nil
}

func writeComponentTokenAtomic(dir, path string, payload []byte) error {
	tmp, err := os.CreateTemp(dir, ".component-token-*")
	if err != nil {
		return fmt.Errorf("create component token temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict component token temporary file permissions: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write component token temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close component token temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("atomically replace component token record: %w", err)
	}
	return nil
}

func (s *UnsafeStore) mergeComponentTokenRevocation(record *domain.ComponentTokenRecord) error {
	payload, err := os.ReadFile(s.componentTokenRevocationPath(record.KeyID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read component token revocation: %w", err)
	}
	revokedAt, err := unmarshalComponentTokenRevocation(payload, record.KeyID)
	if err != nil {
		return err
	}
	record.RevokedAt = revokedAt
	return nil
}

// CreateComponentToken stores a component token hash and safe metadata.
func (s *UnsafeStore) CreateComponentToken(_ context.Context, record *domain.ComponentTokenRecord) error {
	payload, err := marshalComponentTokenRecord(record)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := s.ensureComponentTokenDir()
	if err != nil {
		return err
	}
	if err := writeComponentTokenAtomic(dir, s.componentTokenPath(record.KeyID), payload); err != nil {
		return err
	}
	if record.RevokedAt.IsZero() {
		return nil
	}
	marker, err := marshalComponentTokenRevocation(record.KeyID, record.RevokedAt)
	if err != nil {
		return err
	}
	revocationDir, err := s.ensureComponentTokenRevocationDir()
	if err != nil {
		return err
	}
	return writeComponentTokenAtomic(revocationDir, s.componentTokenRevocationPath(record.KeyID), marker)
}

// LookupComponentToken returns a copy of the matching component token record.
func (s *UnsafeStore) LookupComponentToken(_ context.Context, prefix, keyID string) (*domain.ComponentTokenRecord, error) {
	if keyID == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	payload, err := os.ReadFile(s.componentTokenPath(keyID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read component token record: %w", err)
	}
	record, err := unmarshalComponentTokenRecord(payload, keyID)
	if err != nil {
		return nil, err
	}
	if err := s.mergeComponentTokenRevocation(record); err != nil {
		return nil, err
	}
	if record.Prefix != prefix {
		return nil, nil
	}
	return record, nil
}

func (s *UnsafeStore) updateComponentToken(keyID string, update func(*domain.ComponentTokenRecord)) error {
	if keyID == "" {
		return fmt.Errorf("component token key ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.componentTokenPath(keyID)
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("component token not found")
		}
		return fmt.Errorf("read component token record: %w", err)
	}
	record, err := unmarshalComponentTokenRecord(payload, keyID)
	if err != nil {
		return err
	}
	update(record)
	payload, err = marshalComponentTokenRecord(record)
	if err != nil {
		return err
	}
	dir, err := s.ensureComponentTokenDir()
	if err != nil {
		return err
	}
	return writeComponentTokenAtomic(dir, path, payload)
}

// RevokeComponentToken records an authoritative, monotonic revocation marker.
func (s *UnsafeStore) RevokeComponentToken(_ context.Context, keyID string, revokedAt time.Time) error {
	if keyID == "" {
		return fmt.Errorf("component token key ID is required")
	}
	payload, err := marshalComponentTokenRevocation(keyID, revokedAt)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.ReadFile(s.componentTokenPath(keyID)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("component token not found")
		}
		return fmt.Errorf("read component token record: %w", err)
	}
	dir, err := s.ensureComponentTokenRevocationDir()
	if err != nil {
		return err
	}
	return writeComponentTokenAtomic(dir, s.componentTokenRevocationPath(keyID), payload)
}

// UpdateComponentTokenLastUsed records successful component token use.
func (s *UnsafeStore) UpdateComponentTokenLastUsed(_ context.Context, keyID string, lastUsedAt time.Time) error {
	return s.updateComponentToken(keyID, func(record *domain.ComponentTokenRecord) {
		record.LastUsedAt = lastUsedAt
	})
}

// ListComponentTokenMetadata returns sorted safe component token metadata.
func (s *UnsafeStore) ListComponentTokenMetadata(_ context.Context) ([]domain.ComponentTokenMetadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entries, err := os.ReadDir(filepath.Join(s.dataDir, unsafeComponentTokenDir))
	if err != nil {
		if os.IsNotExist(err) {
			return []domain.ComponentTokenMetadata{}, nil
		}
		return nil, fmt.Errorf("read component token directory: %w", err)
	}
	metadata := make([]domain.ComponentTokenMetadata, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(s.dataDir, unsafeComponentTokenDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read component token record: %w", err)
		}
		var data componentTokenData
		if err := json.Unmarshal(payload, &data); err != nil {
			return nil, fmt.Errorf("unmarshal component token record: %w", err)
		}
		record := data.record()
		if err := s.mergeComponentTokenRevocation(record); err != nil {
			return nil, err
		}
		metadata = append(metadata, record.Metadata())
	}
	sort.Slice(metadata, func(i, j int) bool { return metadata[i].KeyID < metadata[j].KeyID })
	return metadata, nil
}

func (s *PassStore) mergeComponentTokenRevocation(ctx context.Context, record *domain.ComponentTokenRecord) error {
	payload, err := s.passShow(ctx, componentTokenRevocationPassPath(record.KeyID))
	if err != nil {
		if isPassEntryNotFound(err) {
			return nil
		}
		return fmt.Errorf("read component token revocation: %w", err)
	}
	revokedAt, err := unmarshalComponentTokenRevocation([]byte(payload), record.KeyID)
	if err != nil {
		return err
	}
	record.RevokedAt = revokedAt
	return nil
}

// CreateComponentToken stores a component token hash and safe metadata in pass.
func (s *PassStore) CreateComponentToken(ctx context.Context, record *domain.ComponentTokenRecord) error {
	payload, err := marshalComponentTokenRecord(record)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	s.componentMu.Lock()
	defer s.componentMu.Unlock()
	if err := s.passInsert(ctx, componentTokenPassPath(record.KeyID), string(payload)); err != nil {
		return fmt.Errorf("store component token record: %w", err)
	}
	if record.RevokedAt.IsZero() {
		return nil
	}
	marker, err := marshalComponentTokenRevocation(record.KeyID, record.RevokedAt)
	if err != nil {
		return err
	}
	if err := s.passInsert(ctx, componentTokenRevocationPassPath(record.KeyID), string(marker)); err != nil {
		return fmt.Errorf("store component token revocation: %w", err)
	}
	return nil
}

// LookupComponentToken finds a component token record by prefix and key ID.
func (s *PassStore) LookupComponentToken(ctx context.Context, prefix, keyID string) (*domain.ComponentTokenRecord, error) {
	if keyID == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	payload, err := s.passShow(ctx, componentTokenPassPath(keyID))
	if err != nil {
		if isPassEntryNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read component token record: %w", err)
	}
	record, err := unmarshalComponentTokenRecord([]byte(payload), keyID)
	if err != nil {
		return nil, err
	}
	if err := s.mergeComponentTokenRevocation(ctx, record); err != nil {
		return nil, err
	}
	if record.Prefix != prefix {
		return nil, nil
	}
	return record, nil
}

func (s *PassStore) updateComponentToken(ctx context.Context, keyID string, update func(*domain.ComponentTokenRecord)) error {
	if keyID == "" {
		return fmt.Errorf("component token key ID is required")
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	s.componentMu.Lock()
	defer s.componentMu.Unlock()
	payload, err := s.passShow(ctx, componentTokenPassPath(keyID))
	if err != nil {
		if isPassEntryNotFound(err) {
			return fmt.Errorf("component token not found")
		}
		return fmt.Errorf("read component token record: %w", err)
	}
	record, err := unmarshalComponentTokenRecord([]byte(payload), keyID)
	if err != nil {
		return err
	}
	update(record)
	payloadBytes, err := marshalComponentTokenRecord(record)
	if err != nil {
		return err
	}
	if err := s.passInsert(ctx, componentTokenPassPath(keyID), string(payloadBytes)); err != nil {
		return fmt.Errorf("store component token record: %w", err)
	}
	return nil
}

// RevokeComponentToken records an authoritative pass-backed revocation marker.
func (s *PassStore) RevokeComponentToken(ctx context.Context, keyID string, revokedAt time.Time) error {
	if keyID == "" {
		return fmt.Errorf("component token key ID is required")
	}
	marker, err := marshalComponentTokenRevocation(keyID, revokedAt)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	s.componentMu.Lock()
	defer s.componentMu.Unlock()
	if _, err := s.passShow(ctx, componentTokenPassPath(keyID)); err != nil {
		if isPassEntryNotFound(err) {
			return fmt.Errorf("component token not found")
		}
		return fmt.Errorf("read component token record: %w", err)
	}
	if err := s.passInsert(ctx, componentTokenRevocationPassPath(keyID), string(marker)); err != nil {
		return fmt.Errorf("store component token revocation: %w", err)
	}
	return nil
}

// UpdateComponentTokenLastUsed records successful component token use in pass.
func (s *PassStore) UpdateComponentTokenLastUsed(ctx context.Context, keyID string, lastUsedAt time.Time) error {
	return s.updateComponentToken(ctx, keyID, func(record *domain.ComponentTokenRecord) {
		record.LastUsedAt = lastUsedAt
	})
}

func parsePassComponentTokenListing(output string) ([]string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || ansiRegex.ReplaceAllString(strings.TrimSpace(lines[0]), "") != passComponentTokenPath {
		return nil, fmt.Errorf("invalid component token pass listing")
	}
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(parsePassLsEntries(passComponentTokenPath+"\n"+line)) != 1 {
			return nil, fmt.Errorf("invalid component token pass listing")
		}
	}

	entries := parsePassLsOutput(output)
	for _, entry := range entries {
		// Component-token paths are deterministic SHA-256 key-ID hashes. A
		// different shape indicates a corrupt or malformed pass listing.
		if strings.Contains(entry, "/") || len(entry) != sha256.Size*2 {
			return nil, fmt.Errorf("invalid component token pass listing")
		}
		if _, err := hex.DecodeString(entry); err != nil {
			return nil, fmt.Errorf("invalid component token pass listing")
		}
	}
	return entries, nil
}

// ListComponentTokenMetadata returns sorted safe pass-backed token metadata.
func (s *PassStore) ListComponentTokenMetadata(ctx context.Context) ([]domain.ComponentTokenMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	output, err := s.passList(ctx, passComponentTokenPath)
	if err != nil {
		if isPassEntryNotFound(err) {
			return []domain.ComponentTokenMetadata{}, nil
		}
		return nil, fmt.Errorf("list component token records: %w", err)
	}
	entries, err := parsePassComponentTokenListing(output)
	if err != nil {
		return nil, err
	}
	metadata := make([]domain.ComponentTokenMetadata, 0, len(entries))
	for _, entry := range entries {
		payload, err := s.passShow(ctx, passComponentTokenPath+"/"+entry)
		if err != nil {
			return nil, fmt.Errorf("read component token record: %w", err)
		}
		var data componentTokenData
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return nil, fmt.Errorf("unmarshal component token record: %w", err)
		}
		record := data.record()
		if err := s.mergeComponentTokenRevocation(ctx, record); err != nil {
			return nil, err
		}
		metadata = append(metadata, record.Metadata())
	}
	sort.Slice(metadata, func(i, j int) bool { return metadata[i].KeyID < metadata[j].KeyID })
	return metadata, nil
}
