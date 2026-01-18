package domainsecrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.Default()
}

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
