package registry

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFilesystemStorage(t *testing.T) {
	tmpDir := t.TempDir()

	storage, err := NewFilesystemStorage(tmpDir)

	require.NoError(t, err)
	assert.NotNil(t, storage)
	assert.Equal(t, tmpDir, storage.rootDir)

	// Check that directories were created
	assert.DirExists(t, filepath.Join(tmpDir, "repositories"))
	assert.DirExists(t, filepath.Join(tmpDir, "blobs"))
	assert.DirExists(t, filepath.Join(tmpDir, "uploads"))
}

func TestNewFilesystemStorage_InvalidDir(t *testing.T) {
	// Try to create storage in a file instead of directory
	tmpFile := filepath.Join(t.TempDir(), "file")
	err := os.WriteFile(tmpFile, []byte("test"), 0644)
	require.NoError(t, err)

	_, err = NewFilesystemStorage(filepath.Join(tmpFile, "subdir"))
	assert.Error(t, err)
}

func TestFilesystemStorage_ManifestOperations(t *testing.T) {
	storage := createTestStorage(t)

	name := "myapp"
	reference := "latest"
	contentType := "application/vnd.docker.distribution.manifest.v2+json"
	manifestData := []byte(`{"test": "manifest"}`)

	// Test PutManifest
	err := storage.PutManifest(name, reference, contentType, manifestData)
	assert.NoError(t, err)

	// Test GetManifest
	data, ct, err := storage.GetManifest(name, reference)
	assert.NoError(t, err)
	assert.Equal(t, manifestData, data)
	assert.Equal(t, contentType, ct)

	// Test manifest file exists
	manifestPath := storage.getManifestPath(name, reference)
	assert.FileExists(t, manifestPath)

	// Test DeleteManifest
	err = storage.DeleteManifest(name, reference)
	assert.NoError(t, err)

	// Test GetManifest after deletion
	_, _, err = storage.GetManifest(name, reference)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "manifest not found")
}

func TestFilesystemStorage_GetManifest_NotFound(t *testing.T) {
	storage := createTestStorage(t)

	_, _, err := storage.GetManifest("nonexistent", "latest")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "manifest not found")
}

func TestFilesystemStorage_DeleteManifest_NotFound(t *testing.T) {
	storage := createTestStorage(t)

	err := storage.DeleteManifest("nonexistent", "latest")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "manifest not found")
}

func TestFilesystemStorage_ManifestContentType_Fallback(t *testing.T) {
	storage := createTestStorage(t)

	name := "myapp"
	reference := "latest"
	manifestData := []byte(`{"test": "manifest"}`)

	// Manually create manifest without content type file
	manifestPath := storage.getManifestPath(name, reference)
	err := os.MkdirAll(filepath.Dir(manifestPath), 0755)
	require.NoError(t, err)
	err = os.WriteFile(manifestPath, manifestData, 0644)
	require.NoError(t, err)

	// Should use fallback content type
	data, ct, err := storage.GetManifest(name, reference)
	assert.NoError(t, err)
	assert.Equal(t, manifestData, data)
	assert.Equal(t, "application/vnd.docker.distribution.manifest.v2+json", ct)
}

func TestFilesystemStorage_BlobOperations(t *testing.T) {
	storage := createTestStorage(t)

	digest := "sha256:abc123"
	blobData := "test blob data"

	// Test PutBlob
	err := storage.PutBlob(digest, strings.NewReader(blobData), int64(len(blobData)))
	assert.NoError(t, err)

	// Test BlobExists
	assert.True(t, storage.BlobExists(digest))
	assert.False(t, storage.BlobExists("sha256:nonexistent"))

	// Test GetBlobPath
	path, err := storage.GetBlobPath(digest)
	assert.NoError(t, err)
	assert.FileExists(t, path)

	// Test GetBlob
	reader, err := storage.GetBlob(digest)
	assert.NoError(t, err)
	defer reader.Close()

	readData, err := io.ReadAll(reader)
	assert.NoError(t, err)
	assert.Equal(t, blobData, string(readData))

	// Test DeleteBlob
	err = storage.DeleteBlob(digest)
	assert.NoError(t, err)

	// Test GetBlob after deletion
	_, err = storage.GetBlob(digest)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "blob not found")
}

func TestFilesystemStorage_GetBlob_NotFound(t *testing.T) {
	storage := createTestStorage(t)

	_, err := storage.GetBlob("sha256:nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "blob not found")
}

func TestFilesystemStorage_GetBlobPath_NotFound(t *testing.T) {
	storage := createTestStorage(t)

	_, err := storage.GetBlobPath("sha256:nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "blob not found")
}

func TestFilesystemStorage_DeleteBlob_NotFound(t *testing.T) {
	storage := createTestStorage(t)

	err := storage.DeleteBlob("sha256:nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "blob not found")
}

func TestFilesystemStorage_PutBlob_SizeMismatch(t *testing.T) {
	storage := createTestStorage(t)

	digest := "sha256:abc123"
	blobData := "test blob data"
	wrongSize := int64(100) // Wrong size

	err := storage.PutBlob(digest, strings.NewReader(blobData), wrongSize)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "blob size mismatch")
}

func TestFilesystemStorage_BlobUploadOperations(t *testing.T) {
	storage := createTestStorage(t)

	name := "myapp"

	// Test StartBlobUpload
	uuid, err := storage.StartBlobUpload(name)
	assert.NoError(t, err)
	assert.NotEmpty(t, uuid)

	// Test upload file exists
	uploadPath := storage.getUploadPath(uuid)
	assert.FileExists(t, uploadPath)

	// Test AppendBlobChunk
	chunk1 := []byte("chunk1")
	length1, err := storage.AppendBlobChunk(name, uuid, chunk1)
	assert.NoError(t, err)
	assert.Equal(t, int64(len(chunk1)), length1)

	chunk2 := []byte("chunk2")
	length2, err := storage.AppendBlobChunk(name, uuid, chunk2)
	assert.NoError(t, err)
	assert.Equal(t, int64(len(chunk1)+len(chunk2)), length2)

	// Test GetBlobUpload
	writer, err := storage.GetBlobUpload(uuid)
	assert.NoError(t, err)
	writer.Close()

	// Test FinishBlobUpload
	digest := "sha256:test123"
	err = storage.FinishBlobUpload(uuid, digest)
	assert.NoError(t, err)

	// Test blob was moved to correct location
	blobPath := storage.getBlobPath(digest)
	assert.FileExists(t, blobPath)

	// Test upload file was removed
	assert.NoFileExists(t, uploadPath)

	// Verify blob content
	data, err := os.ReadFile(blobPath)
	assert.NoError(t, err)
	assert.Equal(t, "chunk1chunk2", string(data))
}

func TestFilesystemStorage_AppendBlobChunk_NotFound(t *testing.T) {
	storage := createTestStorage(t)

	_, err := storage.AppendBlobChunk("myapp", "nonexistent-uuid", []byte("chunk"))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "upload not found")
}

func TestFilesystemStorage_GetBlobUpload_NotFound(t *testing.T) {
	storage := createTestStorage(t)

	_, err := storage.GetBlobUpload("nonexistent-uuid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "upload not found")
}

func TestFilesystemStorage_CancelBlobUpload(t *testing.T) {
	storage := createTestStorage(t)

	name := "myapp"
	uuid, err := storage.StartBlobUpload(name)
	require.NoError(t, err)

	uploadPath := storage.getUploadPath(uuid)
	assert.FileExists(t, uploadPath)

	err = storage.CancelBlobUpload(uuid)
	assert.NoError(t, err)

	// Upload file should be removed
	assert.NoFileExists(t, uploadPath)
}

func TestFilesystemStorage_CancelBlobUpload_NotFound(t *testing.T) {
	storage := createTestStorage(t)

	err := storage.CancelBlobUpload("nonexistent-uuid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "upload not found")
}

func TestFilesystemStorage_TagOperations(t *testing.T) {
	storage := createTestStorage(t)

	name := "myapp"

	// Test ListTags when no tags exist
	tags, err := storage.ListTags(name)
	assert.NoError(t, err)
	assert.Empty(t, tags)

	// Add some manifests to create tags
	err = storage.PutManifest(name, "v1.0", "application/vnd.docker.distribution.manifest.v2+json", []byte(`{"test": "v1.0"}`))
	assert.NoError(t, err)

	err = storage.PutManifest(name, "latest", "application/vnd.docker.distribution.manifest.v2+json", []byte(`{"test": "latest"}`))
	assert.NoError(t, err)

	// Test ListTags after adding manifests
	tags, err = storage.ListTags(name)
	assert.NoError(t, err)
	assert.Len(t, tags, 2)
	assert.Contains(t, tags, "v1.0")
	assert.Contains(t, tags, "latest")

	// Test adding duplicate tag (should not duplicate)
	err = storage.PutManifest(name, "v1.0", "application/vnd.docker.distribution.manifest.v2+json", []byte(`{"test": "v1.0-updated"}`))
	assert.NoError(t, err)

	tags, err = storage.ListTags(name)
	assert.NoError(t, err)
	assert.Len(t, tags, 2) // Should still be 2

	// Test removing a tag
	err = storage.DeleteManifest(name, "v1.0")
	assert.NoError(t, err)

	tags, err = storage.ListTags(name)
	assert.NoError(t, err)
	assert.Len(t, tags, 1)
	assert.Contains(t, tags, "latest")
	assert.NotContains(t, tags, "v1.0")
}

func TestFilesystemStorage_ListRepositories(t *testing.T) {
	storage := createTestStorage(t)

	// Test when no repositories exist
	repos, err := storage.ListRepositories()
	assert.NoError(t, err)
	assert.Empty(t, repos)

	// Add some repositories by creating manifests
	err = storage.PutManifest("app1", "latest", "application/vnd.docker.distribution.manifest.v2+json", []byte(`{"test": "app1"}`))
	assert.NoError(t, err)

	err = storage.PutManifest("namespace/app2", "v1.0", "application/vnd.docker.distribution.manifest.v2+json", []byte(`{"test": "app2"}`))
	assert.NoError(t, err)

	// Test ListRepositories
	repos, err = storage.ListRepositories()
	assert.NoError(t, err)
	assert.Len(t, repos, 2)
	assert.Contains(t, repos, "app1")
	assert.Contains(t, repos, "namespace/app2")
}

func TestFilesystemStorage_PathGenerationMethods(t *testing.T) {
	storage := createTestStorage(t)

	// Test manifest path generation
	manifestPath := storage.getManifestPath("myapp", "latest")
	expected := filepath.Join(storage.rootDir, "repositories", "myapp", "manifests", "latest")
	assert.Equal(t, expected, manifestPath)

	// Test blob path generation with standard digest
	blobPath := storage.getBlobPath("sha256:abc123def456")
	expected = filepath.Join(storage.rootDir, "blobs", "sha256", "ab", "abc123def456")
	assert.Equal(t, expected, blobPath)

	// Test blob path generation with short hash
	blobPath = storage.getBlobPath("sha256:a")
	expected = filepath.Join(storage.rootDir, "blobs", "sha256", "a")
	assert.Equal(t, expected, blobPath)

	// Test blob path generation with invalid digest format
	blobPath = storage.getBlobPath("invaliddigest")
	expected = filepath.Join(storage.rootDir, "blobs", "invaliddigest")
	assert.Equal(t, expected, blobPath)

	// Test upload path generation
	uploadPath := storage.getUploadPath("test-uuid")
	expected = filepath.Join(storage.rootDir, "uploads", "test-uuid")
	assert.Equal(t, expected, uploadPath)

	// Test tags path generation
	tagsPath := storage.getTagsPath("myapp")
	expected = filepath.Join(storage.rootDir, "repositories", "myapp", "tags.json")
	assert.Equal(t, expected, tagsPath)

	// Test content type path generation
	ctPath := storage.getManifestContentTypePath("myapp", "latest")
	expected = filepath.Join(storage.rootDir, "repositories", "myapp", "manifests", "latest.contenttype")
	assert.Equal(t, expected, ctPath)
}

func TestFilesystemStorage_ContentTypeOperations(t *testing.T) {
	storage := createTestStorage(t)

	name := "myapp"
	reference := "latest"
	contentType := "application/vnd.oci.image.manifest.v1+json"

	// Create the directory structure first
	manifestPath := storage.getManifestPath(name, reference)
	err := os.MkdirAll(filepath.Dir(manifestPath), 0755)
	require.NoError(t, err)

	// Test putManifestContentType
	err = storage.putManifestContentType(name, reference, contentType)
	assert.NoError(t, err)

	// Test getManifestContentType
	ct, err := storage.getManifestContentType(name, reference)
	assert.NoError(t, err)
	assert.Equal(t, contentType, ct)

	// Test deleteManifestContentType
	err = storage.deleteManifestContentType(name, reference)
	assert.NoError(t, err)

	// Test getManifestContentType after deletion
	_, err = storage.getManifestContentType(name, reference)
	assert.Error(t, err)

	// Test deleting non-existent content type (should not error)
	err = storage.deleteManifestContentType(name, reference)
	assert.NoError(t, err)
}

func TestFilesystemStorage_TagsManagement(t *testing.T) {
	storage := createTestStorage(t)

	name := "myapp"
	tags := []string{"v1.0", "latest", "stable"}

	// Test saveTagsList
	err := storage.saveTagsList(name, tags)
	assert.NoError(t, err)

	// Test reading tags back
	readTags, err := storage.ListTags(name)
	assert.NoError(t, err)
	assert.Equal(t, tags, readTags)

	// Test updateTagsList (add new tag)
	err = storage.updateTagsList(name, "v2.0")
	assert.NoError(t, err)

	readTags, err = storage.ListTags(name)
	assert.NoError(t, err)
	assert.Len(t, readTags, 4)
	assert.Contains(t, readTags, "v2.0")

	// Test updateTagsList (add existing tag - should not duplicate)
	err = storage.updateTagsList(name, "v1.0")
	assert.NoError(t, err)

	readTags, err = storage.ListTags(name)
	assert.NoError(t, err)
	assert.Len(t, readTags, 4) // Should still be 4

	// Test removeFromTagsList
	err = storage.removeFromTagsList(name, "v1.0")
	assert.NoError(t, err)

	readTags, err = storage.ListTags(name)
	assert.NoError(t, err)
	assert.Len(t, readTags, 3)
	assert.NotContains(t, readTags, "v1.0")

	// Test removing non-existent tag
	err = storage.removeFromTagsList(name, "nonexistent")
	assert.NoError(t, err)

	readTags, err = storage.ListTags(name)
	assert.NoError(t, err)
	assert.Len(t, readTags, 3) // Should still be 3
}

// Helper function to create test storage
func createTestStorage(t *testing.T) *FilesystemStorage {
	tmpDir := t.TempDir()
	storage, err := NewFilesystemStorage(tmpDir)
	require.NoError(t, err)
	return storage
}
