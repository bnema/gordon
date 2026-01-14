package filesystem

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

func TestNewBlobStorage(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)

	require.NoError(t, err)
	assert.NotNil(t, storage)

	// Verify directories were created
	assert.DirExists(t, filepath.Join(tmpDir, "blobs"))
	assert.DirExists(t, filepath.Join(tmpDir, "uploads"))
}

func TestNewBlobStorage_InvalidPath(t *testing.T) {
	log := testLogger()

	// Try to create in a path that can't be created (no permissions)
	_, err := NewBlobStorage("/nonexistent/deeply/nested/path/that/should/fail", log)

	assert.Error(t, err)
}

func TestBlobStorage_PutAndGetBlob(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	blobData := []byte("test blob content")
	digest := "sha256:abc123def456"

	// Put blob
	err = storage.PutBlob(digest, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Get blob
	reader, err := storage.GetBlob(digest)
	require.NoError(t, err)
	defer reader.Close()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	assert.Equal(t, blobData, data)
}

func TestBlobStorage_GetBlob_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	reader, err := storage.GetBlob("sha256:notexists")

	assert.Error(t, err)
	assert.Nil(t, reader)
	assert.Contains(t, err.Error(), "blob not found")
}

func TestBlobStorage_GetBlobPath(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	blobData := []byte("test blob content")
	digest := "sha256:abc123def456"

	// Put blob first
	err = storage.PutBlob(digest, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Get blob path
	path, err := storage.GetBlobPath(digest)
	require.NoError(t, err)
	assert.NotEmpty(t, path)

	// Verify file exists at path
	_, err = os.Stat(path)
	assert.NoError(t, err)
}

func TestBlobStorage_GetBlobPath_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	path, err := storage.GetBlobPath("sha256:notexists")

	assert.Error(t, err)
	assert.Empty(t, path)
	assert.Contains(t, err.Error(), "blob not found")
}

func TestBlobStorage_PutBlob_SizeMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	blobData := []byte("test blob content")
	digest := "sha256:abc123"

	// Put blob with wrong size
	err = storage.PutBlob(digest, bytes.NewReader(blobData), 999)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "size mismatch")
}

func TestBlobStorage_PutBlob_ZeroSize(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	blobData := []byte("test blob content")
	digest := "sha256:abc123"

	// Put blob with size=0 (should skip size check)
	err = storage.PutBlob(digest, bytes.NewReader(blobData), 0)

	assert.NoError(t, err)

	// Verify blob was stored
	assert.True(t, storage.BlobExists(digest))
}

func TestBlobStorage_DeleteBlob(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	blobData := []byte("test blob content")
	digest := "sha256:abc123"

	// Put blob
	err = storage.PutBlob(digest, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Verify it exists
	assert.True(t, storage.BlobExists(digest))

	// Delete blob
	err = storage.DeleteBlob(digest)
	require.NoError(t, err)

	// Verify it's gone
	assert.False(t, storage.BlobExists(digest))
}

func TestBlobStorage_DeleteBlob_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	err = storage.DeleteBlob("sha256:notexists")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "blob not found")
}

func TestBlobStorage_BlobExists(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	digest := "sha256:abc123"

	// Should not exist initially
	assert.False(t, storage.BlobExists(digest))

	// Put blob
	blobData := []byte("test blob content")
	err = storage.PutBlob(digest, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Should exist now
	assert.True(t, storage.BlobExists(digest))
}

func TestBlobStorage_StartBlobUpload(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	uuid, err := storage.StartBlobUpload("myapp")

	require.NoError(t, err)
	assert.NotEmpty(t, uuid)
	assert.Contains(t, uuid, "myapp")

	// Verify upload file was created
	uploadPath := filepath.Join(tmpDir, "uploads", uuid)
	_, err = os.Stat(uploadPath)
	assert.NoError(t, err)
}

func TestBlobStorage_AppendBlobChunk(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	uuid, err := storage.StartBlobUpload("myapp")
	require.NoError(t, err)

	// Append first chunk
	chunk1 := []byte("first chunk")
	size1, err := storage.AppendBlobChunk("myapp", uuid, chunk1)
	require.NoError(t, err)
	assert.Equal(t, int64(len(chunk1)), size1)

	// Append second chunk
	chunk2 := []byte(" second chunk")
	size2, err := storage.AppendBlobChunk("myapp", uuid, chunk2)
	require.NoError(t, err)
	assert.Equal(t, int64(len(chunk1)+len(chunk2)), size2)
}

func TestBlobStorage_AppendBlobChunk_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	_, err = storage.AppendBlobChunk("myapp", "nonexistent-uuid", []byte("data"))

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "upload not found")
}

func TestBlobStorage_GetBlobUpload(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	uuid, err := storage.StartBlobUpload("myapp")
	require.NoError(t, err)

	writer, err := storage.GetBlobUpload(uuid)
	require.NoError(t, err)
	defer writer.Close()

	// Write some data
	_, err = writer.Write([]byte("test data"))
	assert.NoError(t, err)
}

func TestBlobStorage_GetBlobUpload_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	writer, err := storage.GetBlobUpload("nonexistent-uuid")

	assert.Error(t, err)
	assert.Nil(t, writer)
	assert.Contains(t, err.Error(), "upload not found")
}

func TestBlobStorage_FinishBlobUpload(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	// Start upload
	uuid, err := storage.StartBlobUpload("myapp")
	require.NoError(t, err)

	// Append data
	blobData := []byte("complete blob content")
	_, err = storage.AppendBlobChunk("myapp", uuid, blobData)
	require.NoError(t, err)

	// Finish upload
	digest := "sha256:finished123"
	err = storage.FinishBlobUpload(uuid, digest)
	require.NoError(t, err)

	// Verify blob exists at final location
	assert.True(t, storage.BlobExists(digest))

	// Verify upload file is gone
	uploadPath := filepath.Join(tmpDir, "uploads", uuid)
	_, err = os.Stat(uploadPath)
	assert.True(t, os.IsNotExist(err))
}

func TestBlobStorage_CancelBlobUpload(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	// Start upload
	uuid, err := storage.StartBlobUpload("myapp")
	require.NoError(t, err)

	// Verify upload file exists
	uploadPath := filepath.Join(tmpDir, "uploads", uuid)
	_, err = os.Stat(uploadPath)
	require.NoError(t, err)

	// Cancel upload
	err = storage.CancelBlobUpload(uuid)
	require.NoError(t, err)

	// Verify upload file is gone
	_, err = os.Stat(uploadPath)
	assert.True(t, os.IsNotExist(err))
}

func TestBlobStorage_CancelBlobUpload_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	err = storage.CancelBlobUpload("nonexistent-uuid")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "upload not found")
}

func TestBlobStorage_BlobPathStructure(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	// Test that blobs are stored with proper directory structure
	blobData := []byte("test blob content")
	digest := "sha256:abcdef123456"

	err = storage.PutBlob(digest, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Check the path structure: blobs/sha256/ab/abcdef123456
	expectedPath := filepath.Join(tmpDir, "blobs", "sha256", "ab", "abcdef123456")
	_, err = os.Stat(expectedPath)
	assert.NoError(t, err)
}

func TestBlobStorage_DigestFormats(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	tests := []struct {
		name   string
		digest string
	}{
		{"sha256 standard", "sha256:abc123def456"},
		{"sha512", "sha512:xyz789abc"},
		{"short hash", "sha256:ab"},
		{"long hash", "sha256:abcdefghijklmnopqrstuvwxyz0123456789"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blobData := []byte("test content for " + tt.digest)

			err := storage.PutBlob(tt.digest, bytes.NewReader(blobData), int64(len(blobData)))
			require.NoError(t, err)

			assert.True(t, storage.BlobExists(tt.digest))

			reader, err := storage.GetBlob(tt.digest)
			require.NoError(t, err)
			defer reader.Close()

			data, err := io.ReadAll(reader)
			require.NoError(t, err)
			assert.Equal(t, blobData, data)
		})
	}
}

func TestBlobStorage_CompleteUploadFlow(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	// Simulate a complete upload flow
	repoName := "myorg/myapp"
	digest := "sha256:completedupload123"

	// 1. Start upload
	uuid, err := storage.StartBlobUpload(repoName)
	require.NoError(t, err)

	// 2. Send multiple chunks
	chunks := [][]byte{
		[]byte("first chunk of data"),
		[]byte(" - middle chunk"),
		[]byte(" - final chunk"),
	}

	var totalSize int64
	for _, chunk := range chunks {
		size, err := storage.AppendBlobChunk(repoName, uuid, chunk)
		require.NoError(t, err)
		totalSize = size
	}

	expectedTotal := int64(len(chunks[0]) + len(chunks[1]) + len(chunks[2]))
	assert.Equal(t, expectedTotal, totalSize)

	// 3. Finish upload
	err = storage.FinishBlobUpload(uuid, digest)
	require.NoError(t, err)

	// 4. Verify blob is accessible
	reader, err := storage.GetBlob(digest)
	require.NoError(t, err)
	defer reader.Close()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	expectedData := "first chunk of data - middle chunk - final chunk"
	assert.Equal(t, expectedData, string(data))
}
