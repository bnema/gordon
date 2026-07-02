package componentauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestService_CreateTokenStoresHashNotPlaintext(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store, time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC))

	result, err := svc.CreateToken(context.Background(), CreateRequest{
		Name: "runtime-a",
		Role: domain.ComponentRoleRuntime,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Token)
	assert.True(t, strings.HasPrefix(result.Token, "gordon_component."))
	assert.Equal(t, domain.ComponentRoleRuntime, result.Metadata.Role)
	assert.Equal(t, domain.DefaultComponentScopesForRole(domain.ComponentRoleRuntime), result.Metadata.Scopes)

	record := store.onlyRecord(t)
	assert.Equal(t, result.Metadata.KeyID, record.KeyID)
	assert.NotEmpty(t, record.TokenHash)
	assert.NotEqual(t, result.Token, record.TokenHash)
	assert.NotContains(t, record.TokenHash, result.Token)
	assert.NotContains(t, record.TokenHash, tokenSecretPart(t, result.Token))
}

func TestService_CreateTokenRejectsInvalidRoleAndScopes(t *testing.T) {
	tests := []struct {
		name string
		req  CreateRequest
	}{
		{
			name: "invalid role",
			req:  CreateRequest{Name: "bad", Role: domain.ComponentRole("unknown")},
		},
		{
			name: "unknown scope",
			req:  CreateRequest{Name: "bad", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{"routes:unknown"}},
		},
		{
			name: "scope not allowed for role",
			req:  CreateRequest{Name: "bad", Role: domain.ComponentRoleEdge, Scopes: []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			svc := newTestService(store, time.Now().UTC())

			_, err := svc.CreateToken(context.Background(), tt.req)
			require.Error(t, err)
			assert.Empty(t, store.records, "invalid request should not be stored")
		})
	}
}

func TestService_ValidateTokenReturnsIdentityAndUpdatesLastUsed(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	svc := newTestService(store, now)
	result, err := svc.CreateToken(context.Background(), CreateRequest{Name: "registry-a", Role: domain.ComponentRoleRegistry})
	require.NoError(t, err)

	store.now = now.Add(10 * time.Minute)
	identity, err := svc.ValidateToken(context.Background(), result.Token, domain.ComponentScopeRegistryStatus)
	require.NoError(t, err)
	assert.Equal(t, result.Metadata.KeyID, identity.KeyID)
	assert.Equal(t, "registry-a", identity.Name)
	assert.Equal(t, domain.ComponentRoleRegistry, identity.Role)
	assert.Equal(t, store.now, store.lastUsed[result.Metadata.KeyID])
}

func TestService_ValidateTokenRejectsWrongToken(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store, time.Now().UTC())
	result, err := svc.CreateToken(context.Background(), CreateRequest{Name: "edge-a", Role: domain.ComponentRoleEdge})
	require.NoError(t, err)

	parts := strings.Split(result.Token, ".")
	parts[2] = "wrong-secret"
	_, err = svc.ValidateToken(context.Background(), strings.Join(parts, "."), domain.ComponentScopeRoutesWatch)
	assert.ErrorIs(t, err, domain.ErrInvalidToken)
}

func TestService_ValidateTokenRejectsWrongScope(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store, time.Now().UTC())
	result, err := svc.CreateToken(context.Background(), CreateRequest{Name: "edge-a", Role: domain.ComponentRoleEdge})
	require.NoError(t, err)

	_, err = svc.ValidateToken(context.Background(), result.Token, domain.ComponentScopeRuntimeDeploy)
	assert.ErrorIs(t, err, domain.ErrUnauthorized)
}

func TestService_ValidateTokenRejectsRevokedToken(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	svc := newTestService(store, now)
	result, err := svc.CreateToken(context.Background(), CreateRequest{Name: "runtime-a", Role: domain.ComponentRoleRuntime})
	require.NoError(t, err)
	require.NoError(t, svc.RevokeToken(context.Background(), result.Metadata.KeyID))

	_, err = svc.ValidateToken(context.Background(), result.Token, domain.ComponentScopeRuntimeStatus)
	assert.ErrorIs(t, err, domain.ErrRevokedToken)
}

func TestService_ValidateTokenRejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	store := newFakeStore()
	svc := newTestService(store, now)
	result, err := svc.CreateToken(context.Background(), CreateRequest{
		Name:      "runtime-a",
		Role:      domain.ComponentRoleRuntime,
		ExpiresAt: now.Add(time.Hour),
	})
	require.NoError(t, err)

	store.now = now.Add(time.Hour)
	_, err = svc.ValidateToken(context.Background(), result.Token, domain.ComponentScopeRuntimeStatus)
	assert.ErrorIs(t, err, domain.ErrExpiredToken)
}

func TestService_ListTokenMetadataDoesNotExposeTokenMaterial(t *testing.T) {
	store := newFakeStore()
	svc := newTestService(store, time.Now().UTC())
	result, err := svc.CreateToken(context.Background(), CreateRequest{Name: "control-a", Role: domain.ComponentRoleControl})
	require.NoError(t, err)

	metadata, err := svc.ListTokenMetadata(context.Background())
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	assert.Equal(t, result.Metadata.KeyID, metadata[0].KeyID)
	assert.NotContains(t, dumpMetadata(metadata[0]), result.Token)
	assert.NotContains(t, dumpMetadata(metadata[0]), store.onlyRecord(t).TokenHash)
}

func newTestService(store *fakeComponentTokenStore, now time.Time) *Service {
	store.now = now
	return NewService(store, zerowrap.Default(), Config{Now: func() time.Time { return store.now }})
}

func tokenSecretPart(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	return parts[2]
}

func dumpMetadata(metadata domain.ComponentTokenMetadata) string {
	return metadata.KeyID + metadata.Prefix + metadata.Name + string(metadata.Role)
}

type fakeComponentTokenStore struct {
	records  map[string]*domain.ComponentTokenRecord
	lastUsed map[string]time.Time
	now      time.Time
}

func newFakeStore() *fakeComponentTokenStore {
	return &fakeComponentTokenStore{
		records:  make(map[string]*domain.ComponentTokenRecord),
		lastUsed: make(map[string]time.Time),
	}
}

func (f *fakeComponentTokenStore) CreateComponentToken(_ context.Context, record *domain.ComponentTokenRecord) error {
	copy := *record
	copy.Scopes = append([]domain.ComponentScope(nil), record.Scopes...)
	f.records[record.KeyID] = &copy
	return nil
}

func (f *fakeComponentTokenStore) LookupComponentToken(_ context.Context, prefix, keyID string) (*domain.ComponentTokenRecord, error) {
	record := f.records[keyID]
	if record == nil || record.Prefix != prefix {
		return nil, nil
	}
	copy := *record
	copy.Scopes = append([]domain.ComponentScope(nil), record.Scopes...)
	return &copy, nil
}

func (f *fakeComponentTokenStore) RevokeComponentToken(_ context.Context, keyID string, revokedAt time.Time) error {
	record := f.records[keyID]
	if record == nil {
		return errors.New("not found")
	}
	record.RevokedAt = revokedAt
	return nil
}

func (f *fakeComponentTokenStore) UpdateComponentTokenLastUsed(_ context.Context, keyID string, lastUsedAt time.Time) error {
	record := f.records[keyID]
	if record == nil {
		return errors.New("not found")
	}
	record.LastUsedAt = lastUsedAt
	f.lastUsed[keyID] = lastUsedAt
	return nil
}

func (f *fakeComponentTokenStore) ListComponentTokenMetadata(_ context.Context) ([]domain.ComponentTokenMetadata, error) {
	metadata := make([]domain.ComponentTokenMetadata, 0, len(f.records))
	for _, record := range f.records {
		metadata = append(metadata, record.Metadata())
	}
	return metadata, nil
}

func (f *fakeComponentTokenStore) onlyRecord(t *testing.T) *domain.ComponentTokenRecord {
	t.Helper()
	require.Len(t, f.records, 1)
	for _, record := range f.records {
		return record
	}
	t.Fatal("no records")
	return nil
}
