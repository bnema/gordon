package env

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gordon/internal/config"
)

func TestNewLoader(t *testing.T) {
	cfg := &config.Config{
		Env: config.EnvConfig{
			Dir: "/tmp/env",
		},
	}

	loader := NewLoader(cfg)

	assert.NotNil(t, loader)
	assert.Equal(t, cfg, loader.cfg)
	assert.NotNil(t, loader.secretProviders)
	assert.Empty(t, loader.secretProviders)
}

func TestLoader_RegisterSecretProvider(t *testing.T) {
	cfg := &config.Config{}
	loader := NewLoader(cfg)

	mockProvider := &MockSecretProvider{name: "test"}
	loader.RegisterSecretProvider("test", mockProvider)

	assert.Contains(t, loader.secretProviders, "test")
	assert.Equal(t, mockProvider, loader.secretProviders["test"])
}

func TestLoader_LoadEnvForRoute(t *testing.T) {
	tests := []struct {
		name           string
		domain         string
		fileContent    string
		expectedVars   []string
		expectedError  bool
		setupProvider  bool
		providerSecret string
	}{
		{
			name:         "no env file exists",
			domain:       "app.example.com",
			expectedVars: []string{},
		},
		{
			name:        "basic env file",
			domain:      "app.example.com",
			fileContent: "KEY1=value1\nKEY2=value2\n",
			expectedVars: []string{
				"KEY1=value1",
				"KEY2=value2",
			},
		},
		{
			name: "env file with comments and empty lines",
			domain: "app.example.com",
			fileContent: `# This is a comment
KEY1=value1

# Another comment
KEY2=value2
`,
			expectedVars: []string{
				"KEY1=value1",
				"KEY2=value2",
			},
		},
		{
			name:        "env file with quoted values",
			domain:      "app.example.com",
			fileContent: `KEY1="quoted value"` + "\n" + `KEY2='single quoted'` + "\n",
			expectedVars: []string{
				"KEY1=quoted value",
				"KEY2=single quoted",
			},
		},
		{
			name:           "env file with secret resolution",
			domain:         "app.example.com",
			fileContent:    "SECRET_KEY=${test:secret/path}\nNORMAL_KEY=value\n",
			setupProvider:  true,
			providerSecret: "secret-value",
			expectedVars: []string{
				"SECRET_KEY=secret-value",
				"NORMAL_KEY=value",
			},
		},
		{
			name:        "env file with invalid format",
			domain:      "app.example.com",
			fileContent: "INVALID_LINE_NO_EQUALS\nVALID_KEY=value\n",
			expectedVars: []string{
				"VALID_KEY=value",
			},
		},
		{
			name:           "env file with unknown secret provider",
			domain:         "app.example.com",
			fileContent:    "SECRET_KEY=${unknown:secret/path}\n",
			expectedError:  true,
		},
		{
			name:           "env file with malformed secret syntax",
			domain:         "app.example.com",
			fileContent:    "SECRET_KEY=${malformed\n",
			expectedError:  true,
		},
		{
			name:           "env file with invalid secret format",
			domain:         "app.example.com",
			fileContent:    "SECRET_KEY=${invalid-format}\n",
			expectedError:  true,
		},
		{
			name:        "domain with special characters",
			domain:      "sub.app.example.com:8080",
			fileContent: "KEY1=value1\n",
			expectedVars: []string{
				"KEY1=value1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envDir := "/tmp/env"
			cfg := &config.Config{
				Env: config.EnvConfig{
					Dir: envDir,
				},
			}

			loader := NewLoader(cfg)

			// Setup secret provider if needed
			if tt.setupProvider {
				mockProvider := &MockSecretProvider{
					name:   "test",
					secret: tt.providerSecret,
				}
				loader.RegisterSecretProvider("test", mockProvider)
			}

			// Create env file if content is provided
			if tt.fileContent != "" {
				// Create safe domain name for file
				safeDomain := "app_example_com"
				if tt.domain == "sub.app.example.com:8080" {
					safeDomain = "sub_app_example_com_8080"
				}
				
				// envFile would be: filepath.Join(envDir, safeDomain+".env")
				
				// Create directory and file using real filesystem for this test
				realEnvDir := filepath.Join(os.TempDir(), "gordon-test-env")
				defer os.RemoveAll(realEnvDir)
				
				err := os.MkdirAll(realEnvDir, 0755)
				require.NoError(t, err)
				
				realEnvFile := filepath.Join(realEnvDir, safeDomain+".env")
				err = os.WriteFile(realEnvFile, []byte(tt.fileContent), 0644)
				require.NoError(t, err)
				
				// Update config to use real directory
				cfg.Env.Dir = realEnvDir
			}

			// Execute test
			vars, err := loader.LoadEnvForRoute(tt.domain)

			// Verify results
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedVars, vars)
			}
		})
	}
}

func TestLoader_ResolveSecrets(t *testing.T) {
	cfg := &config.Config{}
	loader := NewLoader(cfg)

	// Register mock secret provider
	mockProvider := &MockSecretProvider{
		name:   "test",
		secret: "secret-value",
	}
	loader.RegisterSecretProvider("test", mockProvider)

	tests := []struct {
		name          string
		input         string
		expected      string
		expectedError bool
	}{
		{
			name:     "no secret syntax",
			input:    "plain-value",
			expected: "plain-value",
		},
		{
			name:     "single secret replacement",
			input:    "${test:secret/path}",
			expected: "secret-value",
		},
		{
			name:     "secret in middle of string",
			input:    "prefix-${test:secret/path}-suffix",
			expected: "prefix-secret-value-suffix",
		},
		{
			name:     "multiple secrets",
			input:    "${test:secret1}-${test:secret2}",
			expected: "secret-value-secret-value",
		},
		{
			name:          "unclosed secret syntax",
			input:         "${test:secret",
			expectedError: true,
		},
		{
			name:          "invalid secret format",
			input:         "${invalid-format}",
			expectedError: true,
		},
		{
			name:          "unknown provider",
			input:         "${unknown:secret/path}",
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := loader.resolveSecrets(tt.input)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestLoader_EnsureEnvDir(t *testing.T) {
	tempDir := os.TempDir()
	envDir := filepath.Join(tempDir, "gordon-test-env-ensure")
	defer os.RemoveAll(envDir)

	cfg := &config.Config{
		Env: config.EnvConfig{
			Dir: envDir,
		},
	}

	loader := NewLoader(cfg)

	// Directory should not exist initially
	_, err := os.Stat(envDir)
	assert.True(t, os.IsNotExist(err))

	// Ensure directory
	err = loader.EnsureEnvDir()
	assert.NoError(t, err)

	// Directory should now exist
	info, err := os.Stat(envDir)
	assert.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())
}

func TestLoader_CreateEnvFilesForRoutes(t *testing.T) {
	tempDir := os.TempDir()
	envDir := filepath.Join(tempDir, "gordon-test-env-create")
	defer os.RemoveAll(envDir)

	cfg := &config.Config{
		Env: config.EnvConfig{
			Dir: envDir,
		},
		Routes: map[string]string{
			"app1.example.com":      "nginx:latest",
			"app2.example.com":      "redis:alpine",
			"api.example.com:8080":  "myapi:v1",
		},
	}

	loader := NewLoader(cfg)

	// Ensure the env directory exists
	err := os.MkdirAll(envDir, 0755)
	require.NoError(t, err)

	err = loader.CreateEnvFilesForRoutes()
	assert.NoError(t, err)

	// Check that files were created
	expectedFiles := map[string]struct{domain, image string}{
		"app1_example_com.env":     {domain: "app1.example.com", image: "nginx:latest"},
		"app2_example_com.env":     {domain: "app2.example.com", image: "redis:alpine"},
		"api_example_com_8080.env": {domain: "api.example.com:8080", image: "myapi:v1"},
	}

	for fileName, expected := range expectedFiles {
		filePath := filepath.Join(envDir, fileName)
		assert.FileExists(t, filePath)

		// Check file content
		content, err := os.ReadFile(filePath)
		assert.NoError(t, err)
		
		expectedContent := "# Environment variables for route: " + expected.domain + "\n# Image: " + expected.image + "\n\n"
		assert.Contains(t, string(content), expectedContent)

		// Check file permissions
		info, err := os.Stat(filePath)
		assert.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestLoader_CreateEnvFilesForRoutes_NoRoutes(t *testing.T) {
	cfg := &config.Config{
		Env: config.EnvConfig{
			Dir: "/tmp/env",
		},
		Routes: map[string]string{},
	}

	loader := NewLoader(cfg)

	err := loader.CreateEnvFilesForRoutes()
	assert.NoError(t, err)
}

func TestLoader_CreateEnvFilesForRoutes_ExistingFile(t *testing.T) {
	tempDir := os.TempDir()
	envDir := filepath.Join(tempDir, "gordon-test-env-existing")
	defer os.RemoveAll(envDir)

	err := os.MkdirAll(envDir, 0755)
	require.NoError(t, err)

	// Create existing file
	existingFile := filepath.Join(envDir, "app_example_com.env")
	err = os.WriteFile(existingFile, []byte("EXISTING=value\n"), 0644)
	require.NoError(t, err)

	cfg := &config.Config{
		Env: config.EnvConfig{
			Dir: envDir,
		},
		Routes: map[string]string{
			"app.example.com": "nginx:latest",
		},
	}

	loader := NewLoader(cfg)

	err = loader.CreateEnvFilesForRoutes()
	assert.NoError(t, err)

	// File should still have original content
	content, err := os.ReadFile(existingFile)
	assert.NoError(t, err)
	assert.Equal(t, "EXISTING=value\n", string(content))
}

func TestLoader_CreateEnvFileForRoute(t *testing.T) {
	tempDir := os.TempDir()
	envDir := filepath.Join(tempDir, "gordon-test-env-single")
	defer os.RemoveAll(envDir)

	cfg := &config.Config{
		Env: config.EnvConfig{
			Dir: envDir,
		},
	}

	loader := NewLoader(cfg)

	domain := "test.example.com"
	image := "test:latest"

	err := loader.CreateEnvFileForRoute(domain, image)
	assert.NoError(t, err)

	// Check file was created
	expectedFile := filepath.Join(envDir, "test_example_com.env")
	assert.FileExists(t, expectedFile)

	// Check content
	content, err := os.ReadFile(expectedFile)
	assert.NoError(t, err)
	
	expectedContent := "# Environment variables for route: test.example.com\n# Image: test:latest\n\n"
	assert.Equal(t, expectedContent, string(content))

	// Test creating the same file again (should skip)
	err = loader.CreateEnvFileForRoute(domain, image)
	assert.NoError(t, err)

	// Content should be unchanged
	content2, err := os.ReadFile(expectedFile)
	assert.NoError(t, err)
	assert.Equal(t, string(content), string(content2))
}

func TestLoader_UpdateConfig(t *testing.T) {
	cfg1 := &config.Config{Env: config.EnvConfig{Dir: "/tmp/env1"}}
	cfg2 := &config.Config{Env: config.EnvConfig{Dir: "/tmp/env2"}}

	loader := NewLoader(cfg1)
	assert.Equal(t, cfg1, loader.cfg)

	loader.UpdateConfig(cfg2)
	assert.Equal(t, cfg2, loader.cfg)
}

func TestLoader_GetEnvFilePath(t *testing.T) {
	cfg := &config.Config{
		Env: config.EnvConfig{
			Dir: "/tmp/env",
		},
	}

	loader := NewLoader(cfg)

	tests := []struct {
		domain   string
		expected string
	}{
		{
			domain:   "app.example.com",
			expected: "/tmp/env/app_example_com.env",
		},
		{
			domain:   "api.example.com:8080",
			expected: "/tmp/env/api_example_com_8080.env",
		},
		{
			domain:   "sub.domain.example.com/path",
			expected: "/tmp/env/sub_domain_example_com_path.env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.domain, func(t *testing.T) {
			result := loader.GetEnvFilePath(tt.domain)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// MockSecretProvider is a mock implementation of SecretProvider for testing
type MockSecretProvider struct {
	name   string
	secret string
	err    error
}

func (m *MockSecretProvider) Name() string {
	return m.name
}

func (m *MockSecretProvider) GetSecret(path string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.secret, nil
}