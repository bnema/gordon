package filesystem

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlobStorage_CleanupStaleUploads_RemovesOldFiles(t *testing.T) {
	tmpDir := t.TempDir()
	bs, err := NewBlobStorage(tmpDir, zerowrap.Default())
	require.NoError(t, err)

	uploadsDir := filepath.Join(tmpDir, "uploads")
	stalePath := filepath.Join(uploadsDir, "stale-uuid-1234")
	require.NoError(t, os.WriteFile(stalePath, make([]byte, 1024), 0600))
	staleTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(stalePath, staleTime, staleTime))

	removed, bytes, err := bs.CleanupStaleUploads(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, int64(1024), bytes)
	assert.NoFileExists(t, stalePath)
}

func TestBlobStorage_CleanupStaleUploads_PreservesRecentFiles(t *testing.T) {
	tmpDir := t.TempDir()
	bs, err := NewBlobStorage(tmpDir, zerowrap.Default())
	require.NoError(t, err)

	uploadsDir := filepath.Join(tmpDir, "uploads")
	recentPath := filepath.Join(uploadsDir, "recent-uuid-5678")
	require.NoError(t, os.WriteFile(recentPath, make([]byte, 512), 0600))

	removed, bytes, err := bs.CleanupStaleUploads(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
	assert.Equal(t, int64(0), bytes)
	assert.FileExists(t, recentPath)
}

func TestBlobStorage_CleanupStaleUploads_EmptyUploadsDir(t *testing.T) {
	tmpDir := t.TempDir()
	bs, err := NewBlobStorage(tmpDir, zerowrap.Default())
	require.NoError(t, err)

	removed, bytes, err := bs.CleanupStaleUploads(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
	assert.Equal(t, int64(0), bytes)
}

func TestBlobStorage_CleanupStaleUploads_MixedAges(t *testing.T) {
	tmpDir := t.TempDir()
	bs, err := NewBlobStorage(tmpDir, zerowrap.Default())
	require.NoError(t, err)

	uploadsDir := filepath.Join(tmpDir, "uploads")

	stalePath := filepath.Join(uploadsDir, "stale-uuid")
	require.NoError(t, os.WriteFile(stalePath, make([]byte, 2048), 0600))
	staleTime := time.Now().Add(-72 * time.Hour)
	require.NoError(t, os.Chtimes(stalePath, staleTime, staleTime))

	recentPath := filepath.Join(uploadsDir, "recent-uuid")
	require.NoError(t, os.WriteFile(recentPath, make([]byte, 512), 0600))

	removed, bytes, err := bs.CleanupStaleUploads(24 * time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)
	assert.Equal(t, int64(2048), bytes)
	assert.NoFileExists(t, stalePath)
	assert.FileExists(t, recentPath)
}
