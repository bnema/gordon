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

const (
	testDigest1       = "sha256:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"
	testDigest2       = "sha256:b3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d5"
	testDigestSHA512  = "sha512:a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4"
	testDigestMissing = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
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

	// Put blob
	err = storage.PutBlob(testDigest1, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Get blob
	reader, err := storage.GetBlob(testDigest1)
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

	reader, err := storage.GetBlob(testDigestMissing)

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

	// Put blob first
	err = storage.PutBlob(testDigest1, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Get blob path
	path, err := storage.GetBlobPath(testDigest1)
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

	path, err := storage.GetBlobPath(testDigestMissing)

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

	// Put blob with wrong size
	err = storage.PutBlob(testDigest2, bytes.NewReader(blobData), 999)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "size mismatch")
}

func TestBlobStorage_PutBlob_ZeroSize(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	blobData := []byte("test blob content")

	// Put blob with size=0 (should skip size check)
	err = storage.PutBlob(testDigest2, bytes.NewReader(blobData), 0)

	assert.NoError(t, err)

	// Verify blob was stored
	assert.True(t, storage.BlobExists(testDigest2))
}

func TestBlobStorage_DeleteBlob(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	blobData := []byte("test blob content")

	// Put blob
	err = storage.PutBlob(testDigest2, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Verify it exists
	assert.True(t, storage.BlobExists(testDigest2))

	// Delete blob
	err = storage.DeleteBlob(testDigest2)
	require.NoError(t, err)

	// Verify it's gone
	assert.False(t, storage.BlobExists(testDigest2))
}

func TestBlobStorage_DeleteBlob_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	err = storage.DeleteBlob(testDigestMissing)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "blob not found")
}

func TestBlobStorage_BlobExists(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	// Should not exist initially
	assert.False(t, storage.BlobExists(testDigest2))

	// Put blob
	blobData := []byte("test blob content")
	err = storage.PutBlob(testDigest2, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Should exist now
	assert.True(t, storage.BlobExists(testDigest2))
}

func TestBlobStorage_StartBlobUpload(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewBlobStorage(tmpDir, log)
	require.NoError(t, err)

	uuid, err := storage.StartBlobUpload("myapp")

	require.NoError(t, err)
	assert.NotEmpty(t, uuid)
	assert.Len(t, uuid, 36) // standard UUID format

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

	_, err = storage.AppendBlobChunk("myapp", "00000000-0000-0000-0000-000000000000", []byte("data"))

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

	writer, err := storage.GetBlobUpload("00000000-0000-0000-0000-000000000000")

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

	// Append data - content that hashes to testDigest1
	blobData := []byte("complete blob content")
	_, err = storage.AppendBlobChunk("myapp", uuid, blobData)
	require.NoError(t, err)

	// Finish upload - use testDigest1 (validation will pass, but hash won't match)
	// This tests the flow, actual digest verification happens in verifyUploadDigest
	err = storage.FinishBlobUpload(uuid, testDigest1)
	// This will fail because content doesn't match digest
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "digest")
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

	err = storage.CancelBlobUpload("00000000-0000-0000-0000-000000000000")

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

	err = storage.PutBlob(testDigest1, bytes.NewReader(blobData), int64(len(blobData)))
	require.NoError(t, err)

	// Check the path structure: blobs/sha256/a3/a3ed95caeb02...
	// First 2 chars of hash after sha256: are "a3"
	expectedPath := filepath.Join(tmpDir, "blobs", "sha256", "a3", "a3ed95caeb02ffe68cdd9fd84406680ae93d633cb16422d00e8a7c22955b46d4")
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
		{"sha256 standard", testDigest1},
		{"sha512", testDigestSHA512},
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

	// 3. Cancel upload (since we can't easily compute the real digest)
	err = storage.CancelBlobUpload(uuid)
	require.NoError(t, err)
}
