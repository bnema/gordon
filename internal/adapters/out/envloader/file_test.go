package envloader

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
)

func TestFileLoader_ResolveSecrets(t *testing.T) {
	// Create a temp directory for tests
	tmpDir := t.TempDir()
	log := zerowrap.New(zerowrap.Config{Level: "fatal"})

	loader, err := NewFileLoader(tmpDir, log)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("no secret syntax returns value unchanged", func(t *testing.T) {
		result, err := loader.resolveSecrets(ctx, "plain-value")
		assert.NoError(t, err)
		assert.Equal(t, "plain-value", result)
	})

	t.Run("unclosed secret syntax returns error", func(t *testing.T) {
		_, err := loader.resolveSecrets(ctx, "${pass:secret")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unclosed secret syntax")
	})

	t.Run("invalid secret syntax without colon returns error", func(t *testing.T) {
		_, err := loader.resolveSecrets(ctx, "${invalid}")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid secret syntax")
	})

	t.Run("unknown provider returns error", func(t *testing.T) {
		_, err := loader.resolveSecrets(ctx, "${unknown:path}")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown secret provider")
	})

	t.Run("pass provider resolves secret", func(t *testing.T) {
		mockPass := mocks.NewMockSecretProvider(t)
		mockPass.EXPECT().Name().Return("pass")
		mockPass.EXPECT().GetSecret(ctx, "company/api-key").Return("secret-value-123", nil)

		loader.RegisterSecretProvider(mockPass)

		result, err := loader.resolveSecrets(ctx, "${pass:company/api-key}")
		assert.NoError(t, err)
		assert.Equal(t, "secret-value-123", result)
	})

	t.Run("sops provider resolves secret", func(t *testing.T) {
		mockSops := mocks.NewMockSecretProvider(t)
		mockSops.EXPECT().Name().Return("sops")
		mockSops.EXPECT().GetSecret(ctx, "secrets.yaml:app.password").Return("sops-secret", nil)

		loader.RegisterSecretProvider(mockSops)

		result, err := loader.resolveSecrets(ctx, "${sops:secrets.yaml:app.password}")
		assert.NoError(t, err)
		assert.Equal(t, "sops-secret", result)
	})

	t.Run("multiple secrets in one value", func(t *testing.T) {
		mockPass := mocks.NewMockSecretProvider(t)
		mockPass.EXPECT().Name().Return("pass")
		mockPass.EXPECT().GetSecret(ctx, "db/user").Return("admin", nil)
		mockPass.EXPECT().GetSecret(ctx, "db/pass").Return("secret123", nil)

		loader.RegisterSecretProvider(mockPass)

		result, err := loader.resolveSecrets(ctx, "postgresql://${pass:db/user}:${pass:db/pass}@localhost:5432/db")
		assert.NoError(t, err)
		assert.Equal(t, "postgresql://admin:secret123@localhost:5432/db", result)
	})

	t.Run("provider error is propagated", func(t *testing.T) {
		mockPass := mocks.NewMockSecretProvider(t)
		mockPass.EXPECT().Name().Return("pass")
		mockPass.EXPECT().GetSecret(ctx, "missing/secret").Return("", assert.AnError)

		loader.RegisterSecretProvider(mockPass)

		_, err := loader.resolveSecrets(ctx, "${pass:missing/secret}")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get secret from provider")
	})
}

func TestFileLoader_LoadEnv(t *testing.T) {
	log := zerowrap.New(zerowrap.Config{Level: "fatal"})
	ctx := context.Background()

	t.Run("missing env file returns empty slice", func(t *testing.T) {
		tmpDir := t.TempDir()
		loader, err := NewFileLoader(tmpDir, log)
		require.NoError(t, err)

		envVars, err := loader.LoadEnv(ctx, "nonexistent.example.com")
		assert.NoError(t, err)
		assert.Empty(t, envVars)
	})

	t.Run("loads simple env file", func(t *testing.T) {
		tmpDir := t.TempDir()
		loader, err := NewFileLoader(tmpDir, log)
		require.NoError(t, err)

		// Create env file
		envContent := `# Comment line
NODE_ENV=production
API_URL=https://api.example.com
`
		envFile := filepath.Join(tmpDir, "app_example_com.env")
		require.NoError(t, os.WriteFile(envFile, []byte(envContent), 0600))

		envVars, err := loader.LoadEnv(ctx, "app.example.com")
		assert.NoError(t, err)
		assert.Len(t, envVars, 2)
		assert.Contains(t, envVars, "NODE_ENV=production")
		assert.Contains(t, envVars, "API_URL=https://api.example.com")
	})

	t.Run("loads env file with secrets", func(t *testing.T) {
		tmpDir := t.TempDir()
		loader, err := NewFileLoader(tmpDir, log)
		require.NoError(t, err)

		// Register mock provider
		mockPass := mocks.NewMockSecretProvider(t)
		mockPass.EXPECT().Name().Return("pass")
		mockPass.EXPECT().GetSecret(ctx, "app/api-key").Return("super-secret-key", nil)
		loader.RegisterSecretProvider(mockPass)

		// Create env file with secret reference
		envContent := `NODE_ENV=production
API_KEY=${pass:app/api-key}
`
		envFile := filepath.Join(tmpDir, "app_example_com.env")
		require.NoError(t, os.WriteFile(envFile, []byte(envContent), 0600))

		envVars, err := loader.LoadEnv(ctx, "app.example.com")
		assert.NoError(t, err)
		assert.Len(t, envVars, 2)
		assert.Contains(t, envVars, "NODE_ENV=production")
		assert.Contains(t, envVars, "API_KEY=super-secret-key")
	})

	t.Run("handles quoted values", func(t *testing.T) {
		tmpDir := t.TempDir()
		loader, err := NewFileLoader(tmpDir, log)
		require.NoError(t, err)

		envContent := `DOUBLE_QUOTED="hello world"
SINGLE_QUOTED='hello world'
`
		envFile := filepath.Join(tmpDir, "app_example_com.env")
		require.NoError(t, os.WriteFile(envFile, []byte(envContent), 0600))

		envVars, err := loader.LoadEnv(ctx, "app.example.com")
		assert.NoError(t, err)
		assert.Len(t, envVars, 2)
		assert.Contains(t, envVars, "DOUBLE_QUOTED=hello world")
		assert.Contains(t, envVars, "SINGLE_QUOTED=hello world")
	})

	t.Run("skips invalid lines", func(t *testing.T) {
		tmpDir := t.TempDir()
		loader, err := NewFileLoader(tmpDir, log)
		require.NoError(t, err)

		envContent := `VALID=value
invalid line without equals
ALSO_VALID=another
`
		envFile := filepath.Join(tmpDir, "app_example_com.env")
		require.NoError(t, os.WriteFile(envFile, []byte(envContent), 0600))

		envVars, err := loader.LoadEnv(ctx, "app.example.com")
		assert.NoError(t, err)
		assert.Len(t, envVars, 2)
		assert.Contains(t, envVars, "VALID=value")
		assert.Contains(t, envVars, "ALSO_VALID=another")
	})
}

func TestFileLoader_GetEnvFilePath(t *testing.T) {
	tmpDir := t.TempDir()
	log := zerowrap.New(zerowrap.Config{Level: "fatal"})

	loader, err := NewFileLoader(tmpDir, log)
	require.NoError(t, err)

	tests := []struct {
		domain   string
		expected string
	}{
		{"app.example.com", "app_example_com.env"},
		{"api.sub.example.com", "api_sub_example_com.env"},
		{"localhost:8080", "localhost_8080.env"},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			result := loader.getEnvFilePath(tt.domain)
			assert.Equal(t, filepath.Join(tmpDir, tt.expected), result)
		})
	}
}
