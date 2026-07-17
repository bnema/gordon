package tokenstore

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

var (
	_ out.ComponentTokenStore = (*UnsafeStore)(nil)
	_ out.ComponentTokenStore = (*PassStore)(nil)
)

func componentRecord(keyID, prefix string) *domain.ComponentTokenRecord {
	return &domain.ComponentTokenRecord{
		KeyID:     keyID,
		Prefix:    prefix,
		Name:      "runtime one",
		Role:      domain.ComponentRoleRuntime,
		Scopes:    []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy},
		TokenHash: "81ecb2a5d9d0f2ee0e8f8f3e1d4c2b0a81ecb2a5d9d0f2ee0e8f8f3e1d4c2b0a",
		CreatedAt: time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
	}
}

func TestUnsafeComponentTokenStoreLifecycleAndPersistence(t *testing.T) {
	dir := t.TempDir()
	store, err := NewComponentTokenStore(domain.SecretsBackendUnsafe, dir, disabledTokenStoreLog())
	require.NoError(t, err)

	record := componentRecord("../unsafe-key", "gct_live")
	require.NoError(t, store.CreateComponentToken(context.Background(), record))
	record.Scopes[0] = domain.ComponentScopeEdgeDrain

	found, err := store.LookupComponentToken(context.Background(), "gct_live", "../unsafe-key")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, domain.ComponentScopeRuntimeDeploy, found.Scopes[0])
	found.Scopes[0] = domain.ComponentScopeEdgeDrain

	foundAgain, err := store.LookupComponentToken(context.Background(), "gct_live", "../unsafe-key")
	require.NoError(t, err)
	assert.Equal(t, domain.ComponentScopeRuntimeDeploy, foundAgain.Scopes[0])

	missing, err := store.LookupComponentToken(context.Background(), "wrong", "../unsafe-key")
	require.NoError(t, err)
	assert.Nil(t, missing)
	missing, err = store.LookupComponentToken(context.Background(), "gct_live", "missing")
	require.NoError(t, err)
	assert.Nil(t, missing)

	lastUsed := time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC)
	revoked := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpdateComponentTokenLastUsed(context.Background(), "../unsafe-key", lastUsed))
	require.NoError(t, store.RevokeComponentToken(context.Background(), "../unsafe-key", revoked))

	metadata, err := store.ListComponentTokenMetadata(context.Background())
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	assert.Equal(t, lastUsed, metadata[0].LastUsedAt)
	assert.Equal(t, revoked, metadata[0].RevokedAt)
	assert.Equal(t, "../unsafe-key", metadata[0].KeyID)

	contents, err := os.ReadFile(filepath.Join(dir, unsafeComponentTokenDir, componentTokenFileName("../unsafe-key")))
	require.NoError(t, err)
	assert.NotContains(t, string(contents), "plaintext-component-secret")
	assert.NotContains(t, string(contents), "\"secret\":")
	assert.NotContains(t, string(contents), "\"token\":")
	assert.Contains(t, string(contents), "token_hash")

	info, err := os.Stat(filepath.Join(dir, unsafeComponentTokenDir, componentTokenFileName("../unsafe-key")))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	componentDir, err := os.Stat(filepath.Join(dir, unsafeComponentTokenDir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), componentDir.Mode().Perm())
}

func TestUnsafeComponentTokenStoreCorruptionAndConcurrentUpdates(t *testing.T) {
	dir := t.TempDir()
	store, err := NewComponentTokenStore(domain.SecretsBackendUnsafe, dir, disabledTokenStoreLog())
	require.NoError(t, err)
	require.NoError(t, store.CreateComponentToken(context.Background(), componentRecord("key", "gct_live")))

	path := filepath.Join(dir, unsafeComponentTokenDir, componentTokenFileName("key"))
	require.NoError(t, os.WriteFile(path, []byte("not-json"), 0600))
	_, err = store.LookupComponentToken(context.Background(), "gct_live", "key")
	require.Error(t, err)

	require.NoError(t, store.CreateComponentToken(context.Background(), componentRecord("key", "gct_live")))
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			errs <- store.UpdateComponentTokenLastUsed(context.Background(), "key", time.Unix(int64(i), 0).UTC())
		}(i)
		go func(i int) {
			defer wg.Done()
			errs <- store.RevokeComponentToken(context.Background(), "key", time.Unix(int64(i), 0).UTC())
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		assert.NoError(t, err)
	}
	metadata, err := store.ListComponentTokenMetadata(context.Background())
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	assert.False(t, metadata[0].LastUsedAt.IsZero())
	assert.False(t, metadata[0].RevokedAt.IsZero())
}

func TestComponentTokenStoreFactoryAndPassLifecycle(t *testing.T) {
	passDir := t.TempDir()
	installFakePass(t, passDir)
	t.Setenv("PASS_STORE_DIR", passDir)

	store, err := NewComponentTokenStore(domain.SecretsBackendPass, "", disabledTokenStoreLog())
	require.NoError(t, err)
	require.NoError(t, store.CreateComponentToken(context.Background(), componentRecord("pass/key", "gct_live")))

	found, err := store.LookupComponentToken(context.Background(), "gct_live", "pass/key")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "81ecb2a5d9d0f2ee0e8f8f3e1d4c2b0a81ecb2a5d9d0f2ee0e8f8f3e1d4c2b0a", found.TokenHash)

	require.NoError(t, store.UpdateComponentTokenLastUsed(context.Background(), "pass/key", time.Unix(1, 0).UTC()))
	require.NoError(t, store.RevokeComponentToken(context.Background(), "pass/key", time.Unix(2, 0).UTC()))
	metadata, err := store.ListComponentTokenMetadata(context.Background())
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	assert.Equal(t, "pass/key", metadata[0].KeyID)
	assert.False(t, metadata[0].RevokedAt.IsZero())

	missing, err := store.LookupComponentToken(context.Background(), "other", "pass/key")
	require.NoError(t, err)
	assert.Nil(t, missing)

	entries, err := os.ReadDir(filepath.Join(passDir, "gordon", "component-tokens"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.NotContains(t, entries[0].Name(), "pass/key")
	content, err := os.ReadFile(filepath.Join(passDir, "gordon", "component-tokens", entries[0].Name()))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "plaintext-component-secret")
	assert.NotContains(t, string(content), "\"secret\":")
}

func TestNewComponentTokenStoreRejectsUnsupportedBackend(t *testing.T) {
	_, err := NewComponentTokenStore(domain.SecretsBackendSops, t.TempDir(), disabledTokenStoreLog())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func disabledTokenStoreLog() zerowrap.Logger {
	return zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard})
}

func installFakePass(t *testing.T, storeDir string) {
	t.Helper()
	binDir := t.TempDir()
	script := `#!/bin/sh
set -eu
root="$PASS_STORE_DIR"
case "$1" in
version) exit 0 ;;
insert) path="$4"; mkdir -p "$(dirname "$root/$path")"; cat > "$root/$path" ;;
show) cat "$root/$2" ;;
ls) echo "$2"; find "$root/$2" -type f -printf '└── %f\n' 2>/dev/null || true ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "pass"), []byte(script), 0700))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NotEmpty(t, strings.TrimSpace(storeDir))
}
