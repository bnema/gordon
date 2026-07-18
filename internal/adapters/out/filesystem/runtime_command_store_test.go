package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntimeCommandResultStorePersistsOnlyTerminalResultsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-command-results.json")
	store, err := NewRuntimeCommandResultStore(RuntimeCommandResultStoreConfig{Path: path, MaxEntries: 1})
	require.NoError(t, err)
	key := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	result := domain.RuntimeCommandResult{CommandID: "command-1", IdempotencyKey: "event-key", Generation: 1, Status: domain.RuntimeCommandStatusSucceeded, StartedAt: time.Now(), CompletedAt: time.Now()}
	require.NoError(t, store.SaveRuntimeCommandResult(context.Background(), key, result))

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	restarted, err := NewRuntimeCommandResultStore(RuntimeCommandResultStoreConfig{Path: path, MaxEntries: 1})
	require.NoError(t, err)
	loaded, found, err := restarted.LoadRuntimeCommandResult(context.Background(), key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, result.Status, loaded.Status)

	require.Error(t, restarted.SaveRuntimeCommandResult(context.Background(), key, domain.RuntimeCommandResult{Status: domain.RuntimeCommandStatusFailed}))
}

func TestRuntimeCommandResultStoreQuarantinesCorruptDataAndFailsHealth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-command-results.json")
	require.NoError(t, os.WriteFile(path, []byte("not-json"), 0600))
	store, err := NewRuntimeCommandResultStore(RuntimeCommandResultStoreConfig{Path: path, MaxCorruptFiles: 1, MaxCorruptBytes: 1024})
	require.NoError(t, err)
	require.Error(t, store.Healthy())
	files, err := filepath.Glob(path + ".*.corrupt")
	require.NoError(t, err)
	require.Len(t, files, 1)
}

func TestRuntimeCommandResultStoreRejectsSymlinkWithoutReadingItsTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime-command-results.json")
	target := filepath.Join(dir, "outside")
	require.NoError(t, os.WriteFile(target, []byte(`{"results":[]}`), 0600))
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	store, err := NewRuntimeCommandResultStore(RuntimeCommandResultStoreConfig{Path: path})
	require.NoError(t, err)
	require.Error(t, store.Healthy())
	contents, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, []byte(`{"results":[]}`), contents)
}
