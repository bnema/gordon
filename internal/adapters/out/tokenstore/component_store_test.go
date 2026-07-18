package tokenstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
	tokenHash := sha256.Sum256([]byte(keyID + ":" + prefix))
	return &domain.ComponentTokenRecord{
		KeyID:     keyID,
		Prefix:    prefix,
		Name:      "runtime one",
		Role:      domain.ComponentRoleRuntime,
		Scopes:    []domain.ComponentScope{domain.ComponentScopeRuntimeDeploy},
		TokenHash: hex.EncodeToString(tokenHash[:]),
		CreatedAt: time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
	}
}

type componentTokenTestFile struct {
	*os.File
	syncErr error
}

func (f componentTokenTestFile) Sync() error {
	if f.syncErr != nil {
		return f.syncErr
	}
	return f.File.Sync()
}

type componentTokenTestDirectory struct {
	syncErr error
}

func (d componentTokenTestDirectory) Sync() error { return d.syncErr }
func (componentTokenTestDirectory) Close() error  { return nil }

func TestUnsafeComponentTokenAtomicWriteFailurePreservesOldRecord(t *testing.T) {
	dir := t.TempDir()
	store, err := NewUnsafeStore(dir, disabledTokenStoreLog())
	require.NoError(t, err)
	record := componentRecord("atomic-key", "gct_live")
	require.NoError(t, store.CreateComponentToken(context.Background(), record))
	path := store.componentTokenPath(record.KeyID)
	old, err := os.ReadFile(path)
	require.NoError(t, err)

	t.Run("temporary sync", func(t *testing.T) {
		ops := newComponentTokenFileOps()
		ops.createTemp = func(dir, pattern string) (componentTokenTempFile, error) {
			file, err := os.CreateTemp(dir, pattern)
			return componentTokenTestFile{File: file, syncErr: context.DeadlineExceeded}, err
		}
		store.componentTokenOps = ops
		err := store.UpdateComponentTokenLastUsed(context.Background(), record.KeyID, time.Unix(1, 0))
		require.ErrorIs(t, err, context.DeadlineExceeded)
		actual, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.Equal(t, old, actual)
	})

	t.Run("rename", func(t *testing.T) {
		ops := newComponentTokenFileOps()
		ops.rename = func(string, string) error { return errors.New("rename failed") }
		store.componentTokenOps = ops
		err := store.UpdateComponentTokenLastUsed(context.Background(), record.KeyID, time.Unix(2, 0))
		require.ErrorContains(t, err, "rename failed")
		actual, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.Equal(t, old, actual)
	})

	t.Run("directory open and sync", func(t *testing.T) {
		ops := newComponentTokenFileOps()
		ops.openDir = func(string) (componentTokenDirectory, error) { return nil, errors.New("open failed") }
		store.componentTokenOps = ops
		err := store.UpdateComponentTokenLastUsed(context.Background(), record.KeyID, time.Unix(3, 0))
		require.ErrorContains(t, err, "open failed")

		ops = newComponentTokenFileOps()
		ops.openDir = func(string) (componentTokenDirectory, error) {
			return componentTokenTestDirectory{syncErr: context.DeadlineExceeded}, nil
		}
		store.componentTokenOps = ops
		err = store.UpdateComponentTokenLastUsed(context.Background(), record.KeyID, time.Unix(4, 0))
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestUnsafeComponentTokenRevocationWriteFailureFailsClosed(t *testing.T) {
	dir := t.TempDir()
	store, err := NewUnsafeStore(dir, disabledTokenStoreLog())
	require.NoError(t, err)
	record := componentRecord("revocation-failure", "gct_live")
	require.NoError(t, store.CreateComponentToken(context.Background(), record))
	ops := newComponentTokenFileOps()
	ops.createTemp = func(dir, pattern string) (componentTokenTempFile, error) {
		file, createErr := os.CreateTemp(dir, pattern)
		return componentTokenTestFile{File: file, syncErr: context.DeadlineExceeded}, createErr
	}
	store.componentTokenOps = ops

	err = store.RevokeComponentToken(context.Background(), record.KeyID, time.Now())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	found, lookupErr := store.LookupComponentToken(context.Background(), record.Prefix, record.KeyID)
	require.ErrorIs(t, lookupErr, context.DeadlineExceeded)
	require.Nil(t, found)
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

func TestUnsafeComponentTokenRevocationSurvivesCrossInstanceLastUsedWrite(t *testing.T) {
	dir := t.TempDir()
	revoker, err := NewUnsafeStore(dir, disabledTokenStoreLog())
	require.NoError(t, err)
	updater, err := NewUnsafeStore(dir, disabledTokenStoreLog())
	require.NoError(t, err)
	require.NoError(t, revoker.CreateComponentToken(context.Background(), componentRecord("race-key", "gct_live")))

	read := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- updater.updateComponentToken("race-key", func(record *domain.ComponentTokenRecord) {
			close(read) // The updater has read the old, unrevoked record.
			<-release
			record.LastUsedAt = time.Unix(1, 0).UTC()
		})
	}()
	<-read
	revokedAt := time.Unix(2, 0).UTC()
	require.NoError(t, revoker.RevokeComponentToken(context.Background(), "race-key", revokedAt))
	close(release)
	require.NoError(t, <-done)

	found, err := revoker.LookupComponentToken(context.Background(), "gct_live", "race-key")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, revokedAt, found.RevokedAt)
	metadata, err := updater.ListComponentTokenMetadata(context.Background())
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	assert.Equal(t, revokedAt, metadata[0].RevokedAt)
}

func TestPassComponentTokenRevocationSurvivesCrossInstanceLastUsedWrite(t *testing.T) {
	passDir := t.TempDir()
	installFakePass(t, passDir)
	t.Setenv("PASS_STORE_DIR", passDir)
	revoker := NewPassStore(disabledTokenStoreLog())
	updater := NewPassStore(disabledTokenStoreLog())
	require.NoError(t, revoker.CreateComponentToken(context.Background(), componentRecord("race-key", "gct_live")))

	read := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- updater.updateComponentToken(context.Background(), "race-key", func(record *domain.ComponentTokenRecord) {
			close(read) // The updater has read the old, unrevoked record.
			<-release
			record.LastUsedAt = time.Unix(1, 0).UTC()
		})
	}()
	<-read
	revokedAt := time.Unix(2, 0).UTC()
	require.NoError(t, revoker.RevokeComponentToken(context.Background(), "race-key", revokedAt))
	close(release)
	require.NoError(t, <-done)

	found, err := revoker.LookupComponentToken(context.Background(), "gct_live", "race-key")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, revokedAt, found.RevokedAt)
	metadata, err := updater.ListComponentTokenMetadata(context.Background())
	require.NoError(t, err)
	require.Len(t, metadata, 1)
	assert.Equal(t, revokedAt, metadata[0].RevokedAt)
}

func TestComponentTokenRevocationMarkersFailClosedAndRemainPrivate(t *testing.T) {
	t.Run("unsafe", func(t *testing.T) {
		dir := t.TempDir()
		store, err := NewUnsafeStore(dir, disabledTokenStoreLog())
		require.NoError(t, err)
		require.NoError(t, store.CreateComponentToken(context.Background(), componentRecord("../key", "gct_live")))
		require.NoError(t, store.RevokeComponentToken(context.Background(), "../key", time.Unix(2, 0).UTC()))

		markerPath := store.componentTokenRevocationPath("../key")
		assert.NotContains(t, markerPath, "../key")
		info, err := os.Stat(markerPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
		info, err = os.Stat(filepath.Dir(markerPath))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
		require.NoError(t, os.WriteFile(markerPath, []byte("corrupt"), 0600))
		_, err = store.LookupComponentToken(context.Background(), "gct_live", "../key")
		require.Error(t, err)
		_, err = store.ListComponentTokenMetadata(context.Background())
		require.Error(t, err)
	})

	t.Run("pass", func(t *testing.T) {
		passDir := t.TempDir()
		installFakePass(t, passDir)
		t.Setenv("PASS_STORE_DIR", passDir)
		store := NewPassStore(disabledTokenStoreLog())
		require.NoError(t, store.CreateComponentToken(context.Background(), componentRecord("../key", "gct_live")))
		require.NoError(t, store.RevokeComponentToken(context.Background(), "../key", time.Unix(2, 0).UTC()))

		markerPath := filepath.Join(passDir, componentTokenRevocationPassPath("../key"))
		assert.NotContains(t, filepath.Base(markerPath), "../key")
		require.NoError(t, os.WriteFile(markerPath, []byte("corrupt"), 0600))
		_, err := store.LookupComponentToken(context.Background(), "gct_live", "../key")
		require.Error(t, err)
		_, err = store.ListComponentTokenMetadata(context.Background())
		require.Error(t, err)
	})
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
	assert.Equal(t, componentRecord("pass/key", "gct_live").TokenHash, found.TokenHash)

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

func TestPassComponentTokenStoreOnlyTreatsStandardMissingEntriesAsAbsent(t *testing.T) {
	passDir := t.TempDir()
	installFakePass(t, passDir)
	t.Setenv("PASS_STORE_DIR", passDir)
	t.Setenv("PASS_FAKE_MODE", "missing")
	store := NewPassStore(disabledTokenStoreLog())

	found, err := store.LookupComponentToken(context.Background(), "gct_live", "missing")
	require.NoError(t, err)
	assert.Nil(t, found)

	metadata, err := store.ListComponentTokenMetadata(context.Background())
	require.NoError(t, err)
	assert.Empty(t, metadata)

	err = store.UpdateComponentTokenLastUsed(context.Background(), "missing", time.Now())
	require.Error(t, err)
	assert.EqualError(t, err, "component token not found")
}

func TestPassComponentTokenStorePropagatesBackendFailures(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "executable failure", mode: "failure"},
		{name: "locked store", mode: "locked"},
		{name: "timeout", mode: "timeout"},
		{name: "record read failure", mode: "read-failure"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passDir := t.TempDir()
			installFakePass(t, passDir)
			t.Setenv("PASS_STORE_DIR", passDir)
			t.Setenv("PASS_FAKE_MODE", tt.mode)
			store := NewPassStore(disabledTokenStoreLog())
			if tt.mode == "timeout" {
				store.timeout = 10 * time.Millisecond
			}

			_, err := store.LookupComponentToken(context.Background(), "gct_live", "key")
			require.Error(t, err)
			assert.NotEqual(t, "component token not found", err.Error())

			err = store.UpdateComponentTokenLastUsed(context.Background(), "key", time.Now())
			require.Error(t, err)
			assert.NotEqual(t, "component token not found", err.Error())

			_, err = store.ListComponentTokenMetadata(context.Background())
			require.Error(t, err)
		})
	}
}

func TestPassComponentTokenStoreRejectsMalformedListings(t *testing.T) {
	passDir := t.TempDir()
	installFakePass(t, passDir)
	t.Setenv("PASS_STORE_DIR", passDir)
	t.Setenv("PASS_FAKE_MODE", "malformed-list")
	store := NewPassStore(disabledTokenStoreLog())

	metadata, err := store.ListComponentTokenMetadata(context.Background())
	require.Error(t, err)
	assert.Nil(t, metadata)
}

func TestPassComponentTokenStoreErrorsDoNotExposeRecordData(t *testing.T) {
	passDir := t.TempDir()
	installFakePass(t, passDir)
	t.Setenv("PASS_STORE_DIR", passDir)
	t.Setenv("PASS_FAKE_MODE", "read-failure")
	store := NewPassStore(disabledTokenStoreLog())

	_, err := store.LookupComponentToken(context.Background(), "gct_live", "key")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), componentRecord("key", "gct_live").TokenHash)
	assert.NotContains(t, err.Error(), "plaintext-component-secret")
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
mode="${PASS_FAKE_MODE:-}"
missing() {
	printf 'Error: %s is not in the password store.\n' "$1" >&2
	exit 1
}
unavailable() {
	printf 'pass store unavailable\n' >&2
	exit 1
}
case "$1" in
version) exit 0 ;;
insert) path="$4"; mkdir -p "$(dirname "$root/$path")"; cat > "$root/$path" ;;
show)
	case "$mode" in
	missing) missing "$2" ;;
	failure|locked|read-failure) unavailable ;;
	timeout) sleep 1 ;;
	esac
	if [ ! -f "$root/$2" ]; then missing "$2"; fi
	cat "$root/$2"
	;;
ls)
	case "$mode" in
	missing) missing "$2" ;;
	failure|locked) unavailable ;;
	timeout) sleep 1 ;;
	malformed-list) printf '%s\nthis is not a pass tree\n' "$2"; exit 0 ;;
	read-failure) printf '%s\n└── %064d\n' "$2" 0; exit 0 ;;
	esac
	echo "$2"
	find "$root/$2" -type f -printf '└── %f\n' 2>/dev/null || true
	;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "pass"), []byte(script), 0700))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	require.NotEmpty(t, strings.TrimSpace(storeDir))
}
