package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMigrationCheckpoint() MigrationCheckpoint {
	return MigrationCheckpoint{MigrationID: "migration-1", StartedAt: time.Now().UTC(), Phase: MigrationPhasePlanned, ComponentGeneration: 1, EnvFileReferences: []string{"/redacted/env"}}
}

func TestMigrationCheckpointAtomicMonotonicAndSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	checkpoint := testMigrationCheckpoint()
	require.NoError(t, store.Save(checkpoint))
	loaded, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, checkpoint.MigrationID, loaded.MigrationID)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	checkpoint.Phase = MigrationPhasePrepared
	checkpoint.ComponentGeneration = 2
	require.NoError(t, store.Save(checkpoint))
	checkpoint.Phase = MigrationPhasePlanned
	assert.Error(t, store.Save(checkpoint))
	assert.NoFileExists(t, filepath.Join(filepath.Dir(path), "data-volume"))
}

func TestMigrationCheckpointRejectsCorruptionAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "checkpoint.json")
	store, err := NewMigrationCheckpointStore(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))
	_, err = store.Load()
	assert.Error(t, err)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Symlink(filepath.Join(dir, "other"), path))
	_, err = store.Load()
	assert.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrNotExist))
}
