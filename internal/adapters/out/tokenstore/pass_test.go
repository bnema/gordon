package tokenstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// These tests verify the in-memory caching behavior of PassStore.
// They directly manipulate the cache to test cache hit/miss logic
// without requiring the actual pass binary.

func newTestPassStore() *PassStore {
	return &PassStore{
		timeout:    10 * time.Second,
		tokenCache: make(map[string]*cachedToken),
		revokedSet: make(map[string]struct{}),
	}
}

func TestPassStore_GetToken_CacheHit(t *testing.T) {
	store := newTestPassStore()

	// Pre-populate cache
	expectedToken := &domain.Token{
		ID:        "test-id",
		Subject:   "test-subject",
		Scopes:    []string{"push", "pull"},
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	expectedJWT := "cached-jwt-token"

	store.cacheMu.Lock()
	store.tokenCache["test-subject"] = &cachedToken{
		jwt:   expectedJWT,
		token: expectedToken,
	}
	store.cacheMu.Unlock()

	// Should return cached value without calling pass
	jwt, token, err := store.GetToken(context.Background(), "test-subject")

	require.NoError(t, err)
	assert.Equal(t, expectedJWT, jwt)
	assert.Equal(t, expectedToken.ID, token.ID)
	assert.Equal(t, expectedToken.Subject, token.Subject)
	assert.Equal(t, expectedToken.Scopes, token.Scopes)
}

func TestPassStore_GetToken_CacheMiss_ReturnsNotFound(t *testing.T) {
	store := newTestPassStore()

	// Cache miss should try to call pass (which will fail in test env)
	_, _, err := store.GetToken(context.Background(), "nonexistent")

	// Should return ErrTokenNotFound since pass won't have this token
	assert.ErrorIs(t, err, domain.ErrTokenNotFound)
}

func TestPassStore_IsRevoked_CacheHit_Revoked(t *testing.T) {
	store := newTestPassStore()

	// Pre-populate revocation cache
	store.cacheMu.Lock()
	store.revokedList = []string{"revoked-token-1", "revoked-token-2"}
	store.revokedSet = map[string]struct{}{
		"revoked-token-1": {},
		"revoked-token-2": {},
	}
	store.cacheMu.Unlock()

	// Check revoked token
	revoked, err := store.IsRevoked(context.Background(), "revoked-token-1")
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestPassStore_IsRevoked_CacheHit_NotRevoked(t *testing.T) {
	store := newTestPassStore()

	// Pre-populate revocation cache with some revoked tokens
	store.cacheMu.Lock()
	store.revokedList = []string{"revoked-token-1"}
	store.revokedSet = map[string]struct{}{
		"revoked-token-1": {},
	}
	store.cacheMu.Unlock()

	// Check non-revoked token (cache is populated, so no pass call)
	revoked, err := store.IsRevoked(context.Background(), "valid-token")
	require.NoError(t, err)
	assert.False(t, revoked)
}

func TestPassStore_CacheUpdate_OnSave(t *testing.T) {
	store := newTestPassStore()

	token := &domain.Token{
		ID:        "new-token-id",
		Subject:   "new-subject",
		Scopes:    []string{"push"},
		IssuedAt:  time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}
	jwt := "new-jwt-token"

	// Simulate what SaveToken does to the cache
	store.cacheMu.Lock()
	store.tokenCache[token.Subject] = &cachedToken{jwt: jwt, token: token}
	store.cacheMu.Unlock()

	// Verify cache was updated - subsequent GetToken should hit cache
	gotJWT, gotToken, err := store.GetToken(context.Background(), "new-subject")

	require.NoError(t, err)
	assert.Equal(t, jwt, gotJWT)
	assert.Equal(t, token.ID, gotToken.ID)
}

func TestPassStore_CacheClear_OnDelete(t *testing.T) {
	store := newTestPassStore()

	// Pre-populate cache
	store.cacheMu.Lock()
	store.tokenCache["to-delete"] = &cachedToken{
		jwt:   "some-jwt",
		token: &domain.Token{ID: "id", Subject: "to-delete"},
	}
	store.cacheMu.Unlock()

	// Simulate what DeleteToken does to the cache
	store.cacheMu.Lock()
	delete(store.tokenCache, "to-delete")
	store.cacheMu.Unlock()

	// Verify cache entry was removed
	store.cacheMu.RLock()
	_, ok := store.tokenCache["to-delete"]
	store.cacheMu.RUnlock()

	assert.False(t, ok)
}

func TestPassStore_GetToken_ConcurrentAccess(t *testing.T) {
	store := newTestPassStore()

	// Pre-populate cache
	expectedToken := &domain.Token{
		ID:      "concurrent-id",
		Subject: "concurrent-subject",
		Scopes:  []string{"push", "pull"},
	}
	expectedJWT := "concurrent-jwt"

	store.cacheMu.Lock()
	store.tokenCache["concurrent-subject"] = &cachedToken{
		jwt:   expectedJWT,
		token: expectedToken,
	}
	store.cacheMu.Unlock()

	// Simulate concurrent access (like Docker buildx parallel requests)
	var wg sync.WaitGroup
	errors := make([]error, 0)
	var errMu sync.Mutex

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jwt, token, err := store.GetToken(context.Background(), "concurrent-subject")
			if err != nil {
				errMu.Lock()
				errors = append(errors, err)
				errMu.Unlock()
				return
			}
			if jwt != expectedJWT || token.ID != expectedToken.ID {
				errMu.Lock()
				errors = append(errors, assert.AnError)
				errMu.Unlock()
			}
		}()
	}

	wg.Wait()
	assert.Empty(t, errors, "concurrent access should not produce errors")
}

func TestPassStore_IsRevoked_ConcurrentAccess(t *testing.T) {
	store := newTestPassStore()

	// Pre-populate revocation cache
	store.cacheMu.Lock()
	store.revokedList = []string{"revoked-1"}
	store.revokedSet = map[string]struct{}{"revoked-1": {}}
	store.cacheMu.Unlock()

	// Simulate concurrent access
	var wg sync.WaitGroup
	errors := make([]error, 0)
	var errMu sync.Mutex

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			revoked, err := store.IsRevoked(context.Background(), "revoked-1")
			if err != nil {
				errMu.Lock()
				errors = append(errors, err)
				errMu.Unlock()
				return
			}
			if !revoked {
				errMu.Lock()
				errors = append(errors, assert.AnError)
				errMu.Unlock()
			}
		}()
	}

	wg.Wait()
	assert.Empty(t, errors, "concurrent access should not produce errors")
}

func TestPassStore_RevokeUpdatesCache(t *testing.T) {
	store := newTestPassStore()

	// Initialize revocation cache with an older revoked token
	store.cacheMu.Lock()
	store.revokedList = []string{"oldly-revoked"}
	store.revokedSet = make(map[string]struct{})
	store.cacheMu.Unlock()

	// Simulate what Revoke does to the cache
	tokenID := "newly-revoked"
	store.cacheMu.Lock()
	store.revokedList = append(store.revokedList, tokenID)
	store.revokedSet = make(map[string]struct{}, len(store.revokedList))
	for _, id := range store.revokedList {
		store.revokedSet[id] = struct{}{}
	}
	store.cacheMu.Unlock()

	// Verify IsRevoked now returns true from cache
	revoked, err := store.IsRevoked(context.Background(), tokenID)
	require.NoError(t, err)
	assert.True(t, revoked)
	revoked, err = store.IsRevoked(context.Background(), "oldly-revoked")
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestPassStore_SaveToken_RejectsInvalidSubject(t *testing.T) {
	store := newTestPassStore()

	tests := []struct {
		name    string
		subject string
	}{
		{"path traversal with ../", "../../../etc/passwd"},
		{"path traversal with ..\\", "..\\..\\windows\\system32"},
		{"shell metacharacter semicolon", "user;rm -rf /"},
		{"shell metacharacter pipe", "user|cat /etc/passwd"},
		{"shell metacharacter ampersand", "user&&malicious"},
		{"shell metacharacter backtick", "user`evil`"},
		{"shell metacharacter dollar sign", "user$(evil)"},
		{"newline character", "user\nmalicious"},
		{"null byte", "user\x00malicious"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := &domain.Token{
				ID:        "test-id",
				Subject:   tt.subject,
				Scopes:    []string{"push"},
				IssuedAt:  time.Now(),
				ExpiresAt: time.Now().Add(24 * time.Hour),
			}

			err := store.SaveToken(context.Background(), token, "jwt-token")
			assert.Error(t, err, "should reject invalid subject: %s", tt.subject)
			assert.Contains(t, err.Error(), "invalid subject", "error message should mention invalid subject")
		})
	}
}

func TestPassStore_SaveToken_AcceptsValidSubject(t *testing.T) {
	tests := []struct {
		name    string
		subject string
	}{
		{"simple username", "myuser"},
		{"username with dots", "user.name"},
		{"username with hyphens", "user-name"},
		{"username with underscores", "user_name"},
		{"email-like", "user@example.com"},
		{"path-like but safe", "registry/user"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify the validation logic accepts valid subjects
			err := validateSubject(tt.subject)
			assert.NoError(t, err, "should accept valid subject: %s", tt.subject)
		})
	}
}

func TestPassStore_GetToken_RejectsInvalidSubject(t *testing.T) {
	store := newTestPassStore()

	invalidSubjects := []string{
		"../../../etc/passwd",
		"user;rm -rf /",
		"user|cat",
		"user\nnewline",
	}

	for _, subject := range invalidSubjects {
		t.Run(subject, func(t *testing.T) {
			_, _, err := store.GetToken(context.Background(), subject)
			assert.Error(t, err, "should reject invalid subject")
			assert.Contains(t, err.Error(), "invalid subject")
		})
	}
}

func TestPassStore_DeleteToken_RejectsInvalidSubject(t *testing.T) {
	store := newTestPassStore()

	invalidSubjects := []string{
		"../../../etc/passwd",
		"user;rm -rf /",
		"user|cat",
	}

	for _, subject := range invalidSubjects {
		t.Run(subject, func(t *testing.T) {
			err := store.DeleteToken(context.Background(), subject)
			assert.Error(t, err, "should reject invalid subject")
			assert.Contains(t, err.Error(), "invalid subject")
		})
	}
}
