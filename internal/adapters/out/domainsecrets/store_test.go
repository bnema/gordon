package domainsecrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestFileStore_PathTraversal(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "domainsecrets-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := NewFileStore(tmpDir, testLogger())
	require.NoError(t, err)

	tests := []struct {
		name    string
		domain  string
		wantErr bool
	}{
		// Valid domains
		{"simple domain", "example.com", false},
		{"subdomain", "sub.example.com", false},
		{"domain with port", "example.com:8080", false},
		{"domain with path", "example.com/path", false},
		{"hyphenated domain", "my-app.example.com", false},
		{"single char", "a", false},

		// Path traversal attempts - should fail
		{"path traversal with dots", "../../../etc/passwd", true},
		{"path traversal in middle", "example.com/../../../etc/passwd", true},
		{"double dots only", "..", true},
		{"path traversal at end", "example.com/..", true},

		// Invalid format - should fail
		{"empty domain", "", true},
		{"starts with dot", ".example.com", true},
		{"ends with dot", "example.com.", true},
		{"special chars semicolon", "example;rm -rf /", true},
		{"null byte", "example\x00.com", true},
		{"newline", "example\n.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test ListKeys
			_, err := store.ListKeys(tt.domain)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, domain.ErrPathTraversal), "expected ErrPathTraversal, got: %v", err)
			} else {
				// No error, or file not found (which is fine)
				if err != nil {
					assert.False(t, errors.Is(err, domain.ErrPathTraversal), "unexpected ErrPathTraversal")
				}
			}

			// Test Set
			err = store.Set(tt.domain, map[string]string{"KEY": "value"})
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, domain.ErrPathTraversal), "expected ErrPathTraversal, got: %v", err)
			} else {
				assert.NoError(t, err)
			}

			// Test GetAll
			_, err = store.GetAll(tt.domain)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, domain.ErrPathTraversal), "expected ErrPathTraversal, got: %v", err)
			}

			// Test Delete
			err = store.Delete(tt.domain, "KEY")
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, domain.ErrPathTraversal), "expected ErrPathTraversal, got: %v", err)
			}
		})
	}
}

func TestFileStore_PathContainment(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "domainsecrets-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := NewFileStore(tmpDir, testLogger())
	require.NoError(t, err)

	// Valid domain - file should be created inside tmpDir
	err = store.Set("example.com", map[string]string{"KEY": "value"})
	require.NoError(t, err)

	// Verify file exists inside tmpDir
	expectedFile := filepath.Join(tmpDir, "example_com.env")
	_, err = os.Stat(expectedFile)
	assert.NoError(t, err, "expected env file to exist at %s", expectedFile)

	// Verify no file was created outside tmpDir
	entries, err := filepath.Glob(tmpDir + "/../*")
	require.NoError(t, err)
	for _, entry := range entries {
		if filepath.Base(entry) != filepath.Base(tmpDir) {
			// Check it's not our test file escaped
			assert.NotContains(t, entry, ".env", "unexpected .env file outside tmpDir: %s", entry)
		}
	}
}

func TestSanitizeDomainForContainer(t *testing.T) {
	tests := []struct {
		domain      string
		expected    string
		description string
	}{
		{
			domain:      "git.example.com",
			expected:    "git__example__com",
			description: "Dots become double underscores",
		},
		{
			domain:      "git-example.com",
			expected:    "git-example__com",
			description: "Hyphens preserved, dots become underscores",
		},
		{
			domain:      "app:8080.example.com",
			expected:    "app-_8080__example__com",
			description: "Colons become hyphen-underscore",
		},
		{
			domain:      "git.example.com:3000",
			expected:    "git__example__com-_3000",
			description: "Multiple separators handled distinctly",
		},
		{
			domain:      "simple.com",
			expected:    "simple__com",
			description: "Simple domain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			result := domain.SanitizeDomainForContainer(tt.domain)
			assert.Equal(t, tt.expected, result, "sanitization should match expected")
		})
	}

	// Verify no collisions between potentially conflicting domains
	t.Run("NoCollisions", func(t *testing.T) {
		domains := []string{
			"git.example.com",
			"git-example.com",
			"app:8080.example.com",
			"app-8080-example.com",
		}

		results := make(map[string]string)
		for _, d := range domains {
			result := domain.SanitizeDomainForContainer(d)
			if original, exists := results[result]; exists {
				t.Errorf("COLLISION: %q and %q both sanitize to %q", original, d, result)
			}
			results[result] = d
		}
	})
}

func TestFileStore_AttachmentOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "domainsecrets-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := NewFileStore(tmpDir, testLogger())
	require.NoError(t, err)

	containerName := "gitea-postgres"

	t.Run("SetAttachment creates new file", func(t *testing.T) {
		err := store.SetAttachment(containerName, map[string]string{
			"POSTGRES_DB":       "gitea",
			"POSTGRES_PASSWORD": "secret123",
		})
		require.NoError(t, err)

		// Verify file exists
		expectedFile := filepath.Join(tmpDir, "gordon-"+containerName+".env")
		_, err = os.Stat(expectedFile)
		assert.NoError(t, err, "expected attachment env file to exist at %s", expectedFile)
	})

	t.Run("GetAllAttachment retrieves secrets", func(t *testing.T) {
		secrets, err := store.GetAllAttachment(containerName)
		require.NoError(t, err)
		assert.Equal(t, "gitea", secrets["POSTGRES_DB"])
		assert.Equal(t, "secret123", secrets["POSTGRES_PASSWORD"])
	})

	t.Run("SetAttachment merges with existing", func(t *testing.T) {
		err := store.SetAttachment(containerName, map[string]string{
			"NEW_VAR": "new_value",
		})
		require.NoError(t, err)

		// Should have both old and new secrets
		secrets, err := store.GetAllAttachment(containerName)
		require.NoError(t, err)
		assert.Equal(t, "gitea", secrets["POSTGRES_DB"])
		assert.Equal(t, "secret123", secrets["POSTGRES_PASSWORD"])
		assert.Equal(t, "new_value", secrets["NEW_VAR"])
	})

	t.Run("GetAllAttachment returns empty map for non-existent container", func(t *testing.T) {
		secrets, err := store.GetAllAttachment("non-existent")
		require.NoError(t, err)
		assert.Empty(t, secrets)
	})

	t.Run("SetAttachment with hyphenated container name", func(t *testing.T) {
		hyphenatedContainer := "gordon-git-example-com-gitea-postgres"
		err := store.SetAttachment(hyphenatedContainer, map[string]string{
			"KEY": "value",
		})
		require.NoError(t, err)

		secrets, err := store.GetAllAttachment(hyphenatedContainer)
		require.NoError(t, err)
		assert.Equal(t, "value", secrets["KEY"])
	})
}

func TestFileStore_AttachmentPathTraversal(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "domainsecrets-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := NewFileStore(tmpDir, testLogger())
	require.NoError(t, err)

	tests := []struct {
		name       string
		container  string
		wantSetErr bool
		wantGetErr bool
	}{
		// Valid container names
		{"simple", "postgres", false, false},
		{"with hyphen", "gitea-postgres", false, false},
		{"with underscore", "gitea_postgres", false, false},
		{"complex real container", "gordon-git-example-com-gitea-postgres", false, false},

		// Invalid container names - should fail
		{"empty", "", true, true},
		{"starts with number", "1container", true, true},
		{"starts with hyphen", "-container", true, true},
		{"starts with underscore", "_container", true, true},
		{"path traversal", "../etc", true, true},
		{"contains slash", "container/name", true, true},
		{"contains backslash", "container\\name", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test SetAttachment
			err := store.SetAttachment(tt.container, map[string]string{"KEY": "value"})
			if tt.wantSetErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Test GetAllAttachment
			_, err = store.GetAllAttachment(tt.container)
			if tt.wantGetErr {
				require.Error(t, err)
			} else {
				if !tt.wantSetErr {
					// Only check no error if we successfully set it
					assert.NoError(t, err)
				}
			}
		})
	}
}

func TestFileStore_ValidDomainOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "domainsecrets-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := NewFileStore(tmpDir, testLogger())
	require.NoError(t, err)

	domain := "test.example.com"

	// Set secrets
	err = store.Set(domain, map[string]string{
		"DB_HOST":     "localhost",
		"DB_PASSWORD": "secret123",
	})
	require.NoError(t, err)

	// Get all secrets
	secrets, err := store.GetAll(domain)
	require.NoError(t, err)
	assert.Equal(t, "localhost", secrets["DB_HOST"])
	assert.Equal(t, "secret123", secrets["DB_PASSWORD"])

	// List keys
	keys, err := store.ListKeys(domain)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Contains(t, keys, "DB_HOST")
	assert.Contains(t, keys, "DB_PASSWORD")

	// Delete a key
	err = store.Delete(domain, "DB_PASSWORD")
	require.NoError(t, err)

	// Verify deletion
	secrets, err = store.GetAll(domain)
	require.NoError(t, err)
	assert.Len(t, secrets, 1)
	assert.Equal(t, "localhost", secrets["DB_HOST"])
	_, exists := secrets["DB_PASSWORD"]
	assert.False(t, exists)
}

func TestFileStore_DeleteAttachment(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "domainsecrets-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := NewFileStore(tmpDir, testLogger())
	require.NoError(t, err)

	containerName := "gitea-postgres"

	// Seed with two secrets
	err = store.SetAttachment(containerName, map[string]string{
		"POSTGRES_USER":     "gitea",
		"POSTGRES_PASSWORD": "secret123",
	})
	require.NoError(t, err)

	// Delete one key
	err = store.DeleteAttachment(containerName, "POSTGRES_PASSWORD")
	require.NoError(t, err)

	// Verify remaining
	secrets, err := store.GetAllAttachment(containerName)
	require.NoError(t, err)
	assert.Len(t, secrets, 1)
	assert.Equal(t, "gitea", secrets["POSTGRES_USER"])
	_, exists := secrets["POSTGRES_PASSWORD"]
	assert.False(t, exists)

	// Delete nonexistent key - should be no-op
	err = store.DeleteAttachment(containerName, "NONEXISTENT_KEY")
	assert.NoError(t, err)

	// Verify secrets unchanged after no-op delete
	secrets, err = store.GetAllAttachment(containerName)
	require.NoError(t, err)
	assert.Len(t, secrets, 1)
	assert.Equal(t, "gitea", secrets["POSTGRES_USER"])
}

func TestGetEnvFilePath(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "domainsecrets-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	store, err := NewFileStore(tmpDir, testLogger())
	require.NoError(t, err)

	tests := []struct {
		domain       string
		wantEmpty    bool
		wantFilename string
	}{
		{"example.com", false, "example_com.env"},
		{"sub.example.com", false, "sub_example_com.env"},
		{"example.com:8080", false, "example_com_8080.env"},
		{"example.com/path", false, "example_com_path.env"},
		{"../evil", true, ""},
		{"", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			path := store.getEnvFilePath(tt.domain)

			if tt.wantEmpty {
				assert.Empty(t, path)
			} else {
				assert.NotEmpty(t, path)
				assert.Equal(t, filepath.Join(tmpDir, tt.wantFilename), path)
			}
		})
	}
}
