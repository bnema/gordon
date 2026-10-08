package secretfiles_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/secretfiles"
	"github.com/bnema/gordon/internal/boundaries/out"
)

func newStore(t *testing.T) (*secretfiles.Store, string) {
	t.Helper()
	dataDir := t.TempDir()
	store, err := secretfiles.NewStore(dataDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store, dataDir
}

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	info, err := os.Stat(p)
	require.NoError(t, err)
	return info.Mode().Perm()
}

func TestStore_WriteModesAndRead(t *testing.T) {
	store, dataDir := newStore(t)
	ctx := context.Background()
	assert.Equal(t, filepath.Join(dataDir, "secrets-run"), store.Dir())

	paths, err := store.Write(ctx, "blog", "gordon-blog--web--a", map[string]string{"DB": "s3cr3t", "API": "k"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(store.Dir(), "blog", "gordon-blog--web--a", "DB"), paths["DB"])
	assert.Len(t, paths, 2)

	assert.Equal(t, os.FileMode(0o700), mode(t, store.Dir()))
	assert.Equal(t, os.FileMode(0o700), mode(t, filepath.Join(store.Dir(), "blog")))
	assert.Equal(t, os.FileMode(0o700), mode(t, filepath.Join(store.Dir(), "blog", "gordon-blog--web--a")))
	assert.Equal(t, os.FileMode(0o444), mode(t, paths["DB"]))

	got, err := store.Read(ctx, "blog", "gordon-blog--web--a", "DB")
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", got)
}

func TestStore_WriteReplacesStaleKeys(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	_, err := store.Write(ctx, "blog", "c1", map[string]string{"OLD": "x"})
	require.NoError(t, err)
	_, err = store.Write(ctx, "blog", "c1", map[string]string{"NEW": "y"})
	require.NoError(t, err)

	_, err = store.Read(ctx, "blog", "c1", "OLD")
	require.Error(t, err)
	got, err := store.Read(ctx, "blog", "c1", "NEW")
	require.NoError(t, err)
	assert.Equal(t, "y", got)
}

func TestStore_RemoveAndList(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	for _, set := range []out.SecretFileSet{{App: "blog", Container: "c2"}, {App: "blog", Container: "c1"}, {App: "alpha", Container: "z"}} {
		_, err := store.Write(ctx, set.App, set.Container, map[string]string{"K": "v"})
		require.NoError(t, err)
	}

	sets, err := store.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []out.SecretFileSet{{App: "alpha", Container: "z"}, {App: "blog", Container: "c1"}, {App: "blog", Container: "c2"}}, sets)

	require.NoError(t, store.Remove(ctx, "blog", "c1"))
	assert.NoDirExists(t, filepath.Join(store.Dir(), "blog", "c1"))
	assert.DirExists(t, filepath.Join(store.Dir(), "blog", "c2"))

	require.NoError(t, store.Remove(ctx, "blog", "c2"))
	assert.NoDirExists(t, filepath.Join(store.Dir(), "blog"))

	require.NoError(t, store.Remove(ctx, "blog", "missing"))
	require.NoError(t, store.Remove(ctx, "ghost", "missing"))

	sets, err = store.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []out.SecretFileSet{{App: "alpha", Container: "z"}}, sets)
}

func TestStore_InvalidNamesRejected(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	cases := []struct {
		name, app, container, key string
	}{
		{"app traversal", "../x", "c", "K"},
		{"app slash", "a/b", "c", "K"},
		{"container dot", "blog", ".", "K"},
		{"container dotdot", "blog", "..", "K"},
		{"container slash", "blog", "a/b", "K"},
		{"container empty", "blog", "", "K"},
		{"bad key", "blog", "c", "bad-KEY!"},
		{"key traversal", "blog", "c", "../K"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.Write(ctx, tc.app, tc.container, map[string]string{tc.key: "v"})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), store.Dir())
		})
	}
	sets, err := store.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, sets)
}

func TestStore_SymlinkEscapeRefused(t *testing.T) {
	store, _ := newStore(t)
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(store.Dir(), "blog")))

	_, err := store.Write(context.Background(), "blog", "c1", map[string]string{"K": "v"})
	require.Error(t, err)
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

func TestNewStore_RefusesSymlinkBase(t *testing.T) {
	dataDir := t.TempDir()
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(dataDir, "secrets-run")))
	_, err := secretfiles.NewStore(dataDir)
	require.Error(t, err)
}
