package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManifestStorage(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)

	require.NoError(t, err)
	assert.NotNil(t, storage)

	// Verify repositories directory was created
	assert.DirExists(t, filepath.Join(tmpDir, "repositories"))
}

func TestNewManifestStorage_InvalidPath(t *testing.T) {
	log := testLogger()

	_, err := NewManifestStorage("/nonexistent/deeply/nested/path/that/should/fail", log)

	assert.Error(t, err)
}

func TestManifestStorage_PutAndGetManifest(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2, "mediaType": "application/vnd.docker.distribution.manifest.v2+json"}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	// Put manifest
	err = storage.PutManifest("myapp", "latest", contentType, manifestData)
	require.NoError(t, err)

	// Get manifest
	data, ct, err := storage.GetManifest("myapp", "latest")
	require.NoError(t, err)

	assert.Equal(t, manifestData, data)
	assert.Equal(t, contentType, ct)
}

func TestManifestStorage_GetManifest_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	data, ct, err := storage.GetManifest("notexists", "latest")

	assert.Error(t, err)
	assert.Nil(t, data)
	assert.Empty(t, ct)
	assert.Contains(t, err.Error(), "manifest not found")
}

func TestManifestStorage_GetManifest_FallbackContentType(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	// Create manifest file directly without content type file
	manifestDir := filepath.Join(tmpDir, "repositories", "myapp", "manifests")
	err = os.MkdirAll(manifestDir, 0750)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2}`)
	err = os.WriteFile(filepath.Join(manifestDir, "latest"), manifestData, 0600)
	require.NoError(t, err)

	// Get manifest should use fallback content type
	data, ct, err := storage.GetManifest("myapp", "latest")
	require.NoError(t, err)

	assert.Equal(t, manifestData, data)
	assert.Equal(t, "application/vnd.docker.distribution.manifest.v2+json", ct)
}

func TestManifestStorage_DeleteManifest(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	// Put manifest
	err = storage.PutManifest("myapp", "latest", contentType, manifestData)
	require.NoError(t, err)

	// Verify it exists
	_, _, err = storage.GetManifest("myapp", "latest")
	require.NoError(t, err)

	// Delete manifest
	err = storage.DeleteManifest("myapp", "latest")
	require.NoError(t, err)

	// Verify it's gone
	_, _, err = storage.GetManifest("myapp", "latest")
	assert.Error(t, err)
}

func TestManifestStorage_DeleteManifest_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	err = storage.DeleteManifest("notexists", "latest")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "manifest not found")
}

func TestManifestStorage_ListTags(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	// Put multiple manifests
	err = storage.PutManifest("myapp", "latest", contentType, manifestData)
	require.NoError(t, err)
	err = storage.PutManifest("myapp", "v1.0", contentType, manifestData)
	require.NoError(t, err)
	err = storage.PutManifest("myapp", "v2.0", contentType, manifestData)
	require.NoError(t, err)

	// List tags
	tags, err := storage.ListTags("myapp")
	require.NoError(t, err)

	assert.Len(t, tags, 3)
	assert.Contains(t, tags, "latest")
	assert.Contains(t, tags, "v1.0")
	assert.Contains(t, tags, "v2.0")
}

func TestManifestStorage_ListTags_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	// List tags for non-existent repo
	tags, err := storage.ListTags("notexists")
	require.NoError(t, err)

	assert.Empty(t, tags)
}

func TestManifestStorage_ListTags_SkipsDigestReference(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	err = storage.PutManifest("myapp", "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", contentType, manifestData)
	require.NoError(t, err)

	tags, err := storage.ListTags("myapp")
	require.NoError(t, err)

	assert.Empty(t, tags)
}

func TestManifestStorage_ListTags_AfterDelete(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	// Put manifests
	err = storage.PutManifest("myapp", "latest", contentType, manifestData)
	require.NoError(t, err)
	err = storage.PutManifest("myapp", "v1.0", contentType, manifestData)
	require.NoError(t, err)

	// Delete one
	err = storage.DeleteManifest("myapp", "v1.0")
	require.NoError(t, err)

	// List tags
	tags, err := storage.ListTags("myapp")
	require.NoError(t, err)

	assert.Len(t, tags, 1)
	assert.Contains(t, tags, "latest")
	assert.NotContains(t, tags, "v1.0")
}

func TestManifestStorage_ListRepositories(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	// Put manifests for different repos
	err = storage.PutManifest("app1", "latest", contentType, manifestData)
	require.NoError(t, err)
	err = storage.PutManifest("app2", "latest", contentType, manifestData)
	require.NoError(t, err)
	err = storage.PutManifest("org/app3", "latest", contentType, manifestData)
	require.NoError(t, err)

	// List repositories
	repos, err := storage.ListRepositories()
	require.NoError(t, err)

	assert.Len(t, repos, 3)
	assert.Contains(t, repos, "app1")
	assert.Contains(t, repos, "app2")
	assert.Contains(t, repos, "org/app3")
}

func TestManifestStorage_ListRepositories_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	repos, err := storage.ListRepositories()
	require.NoError(t, err)

	assert.Empty(t, repos)
}

func TestManifestStorage_NestedRepositoryNames(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	tests := []struct {
		name      string
		reference string
	}{
		{"simple", "latest"},
		{"org/project", "v1.0"},
		{"org/team/project", "v2.0"},
		{"deeply/nested/repo/name", "sha256:abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := storage.PutManifest(tt.name, tt.reference, contentType, manifestData)
			require.NoError(t, err)

			data, ct, err := storage.GetManifest(tt.name, tt.reference)
			require.NoError(t, err)
			assert.Equal(t, manifestData, data)
			assert.Equal(t, contentType, ct)
		})
	}
}

func TestManifestStorage_ContentTypes(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	tests := []struct {
		name        string
		contentType string
	}{
		{"v2 manifest", "application/vnd.docker.distribution.manifest.v2+json"},
		{"v1 manifest", "application/vnd.docker.distribution.manifest.v1+json"},
		{"OCI manifest", "application/vnd.oci.image.manifest.v1+json"},
		{"OCI index", "application/vnd.oci.image.index.v1+json"},
		{"manifest list", "application/vnd.docker.distribution.manifest.list.v2+json"},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoName := "test-repo"
			reference := "tag-" + string(rune('a'+i))
			manifestData := []byte(`{"schemaVersion": 2}`)

			err := storage.PutManifest(repoName, reference, tt.contentType, manifestData)
			require.NoError(t, err)

			_, ct, err := storage.GetManifest(repoName, reference)
			require.NoError(t, err)
			assert.Equal(t, tt.contentType, ct)
		})
	}
}

func TestManifestStorage_DuplicateTag(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	// Put first manifest
	manifestData1 := []byte(`{"schemaVersion": 2, "version": 1}`)
	err = storage.PutManifest("myapp", "latest", contentType, manifestData1)
	require.NoError(t, err)

	// Put second manifest with same tag (overwrite)
	manifestData2 := []byte(`{"schemaVersion": 2, "version": 2}`)
	err = storage.PutManifest("myapp", "latest", contentType, manifestData2)
	require.NoError(t, err)

	// Get manifest should return the latest one
	data, _, err := storage.GetManifest("myapp", "latest")
	require.NoError(t, err)
	assert.Equal(t, manifestData2, data)

	// Tags list should only have one entry
	tags, err := storage.ListTags("myapp")
	require.NoError(t, err)
	assert.Len(t, tags, 1)
}

func TestManifestStorage_LargeManifest(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	// Create a large manifest (simulating one with many layers)
	largeManifest := make([]byte, 1024*1024) // 1MB
	for i := range largeManifest {
		largeManifest[i] = byte('a' + (i % 26))
	}

	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	err = storage.PutManifest("large-app", "latest", contentType, largeManifest)
	require.NoError(t, err)

	data, _, err := storage.GetManifest("large-app", "latest")
	require.NoError(t, err)
	assert.Equal(t, largeManifest, data)
}

func TestManifestStorage_SpecialCharactersInReference(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	manifestData := []byte(`{"schemaVersion": 2}`)
	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	// SHA256 digests are valid references
	reference := "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	err = storage.PutManifest("myapp", reference, contentType, manifestData)
	require.NoError(t, err)

	data, _, err := storage.GetManifest("myapp", reference)
	require.NoError(t, err)
	assert.Equal(t, manifestData, data)
}

func TestManifestStorage_CompleteWorkflow(t *testing.T) {
	tmpDir := t.TempDir()
	log := testLogger()

	storage, err := NewManifestStorage(tmpDir, log)
	require.NoError(t, err)

	contentType := "application/vnd.docker.distribution.manifest.v2+json"

	// Simulate a complete registry workflow
	// 1. Push first version
	v1Data := []byte(`{"schemaVersion": 2, "version": "1.0"}`)
	err = storage.PutManifest("myorg/myapp", "v1.0", contentType, v1Data)
	require.NoError(t, err)
	err = storage.PutManifest("myorg/myapp", "latest", contentType, v1Data)
	require.NoError(t, err)

	// 2. Push second version
	v2Data := []byte(`{"schemaVersion": 2, "version": "2.0"}`)
	err = storage.PutManifest("myorg/myapp", "v2.0", contentType, v2Data)
	require.NoError(t, err)
	err = storage.PutManifest("myorg/myapp", "latest", contentType, v2Data)
	require.NoError(t, err)

	// 3. Verify tags
	tags, err := storage.ListTags("myorg/myapp")
	require.NoError(t, err)
	assert.Len(t, tags, 3)

	// 4. Verify latest points to v2
	data, _, err := storage.GetManifest("myorg/myapp", "latest")
	require.NoError(t, err)
	assert.Equal(t, v2Data, data)

	// 5. Verify v1 is still accessible
	data, _, err = storage.GetManifest("myorg/myapp", "v1.0")
	require.NoError(t, err)
	assert.Equal(t, v1Data, data)

	// 6. Delete old version
	err = storage.DeleteManifest("myorg/myapp", "v1.0")
	require.NoError(t, err)

	// 7. Verify tags updated
	tags, err = storage.ListTags("myorg/myapp")
	require.NoError(t, err)
	assert.Len(t, tags, 2)
	assert.NotContains(t, tags, "v1.0")

	// 8. List repositories
	repos, err := storage.ListRepositories()
	require.NoError(t, err)
	assert.Contains(t, repos, "myorg/myapp")
}
