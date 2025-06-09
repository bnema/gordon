package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gordon/internal/testutils"
)

func TestConfig_Load_ValidBasicConfig(t *testing.T) {
	// Create temporary config file
	configContent := testutils.LoadFixtureConfig(t, "basic.toml")
	
	// Create a temporary directory for the config file
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	// Write config to temporary file
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	// Set viper to use this config file
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	// Load config
	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	
	// Verify server config
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 5000, cfg.Server.RegistryPort)
	assert.Equal(t, "registry.example.com", cfg.Server.RegistryDomain)
	assert.Equal(t, "docker", cfg.Server.Runtime)
	
	// Verify registry auth config
	assert.True(t, cfg.RegistryAuth.Enabled)
	assert.Equal(t, "admin", cfg.RegistryAuth.Username)
	assert.Equal(t, "password123", cfg.RegistryAuth.Password)
	
	// Verify routes
	assert.Len(t, cfg.Routes, 2)
	assert.Equal(t, "nginx:latest", cfg.Routes["app.example.com"])
	assert.Equal(t, "myapi:v1", cfg.Routes["api.example.com"])
	
	// Verify volumes config
	assert.True(t, cfg.Volumes.AutoCreate)
	assert.Equal(t, "gordon", cfg.Volumes.Prefix)
	assert.True(t, cfg.Volumes.Preserve)
	
	// Verify env config
	assert.Equal(t, "/tmp/env", cfg.Env.Dir)
	assert.Contains(t, cfg.Env.Providers, "pass")
	assert.Contains(t, cfg.Env.Providers, "sops")
	
	// Verify logging config
	assert.True(t, cfg.Logging.Enabled)
	assert.Equal(t, "info", cfg.Logging.Level)
}

func TestConfig_Load_MinimalConfig(t *testing.T) {
	configContent := testutils.LoadFixtureConfig(t, "minimal.toml")
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	
	// Should have defaults applied where not specified, but explicit values where set
	// Note: viper defaults may not apply if config structure is partially loaded
	assert.Equal(t, "registry.example.com", cfg.Server.RegistryDomain)
	assert.Equal(t, "docker", cfg.Server.Runtime) // explicitly set in minimal config
	assert.False(t, cfg.RegistryAuth.Enabled) // explicitly set in minimal config
	assert.True(t, cfg.Volumes.AutoCreate) // default
	assert.Equal(t, "gordon", cfg.Volumes.Prefix) // default
}

func TestConfig_Load_InvalidTOMLSyntax(t *testing.T) {
	configContent := testutils.LoadFixtureConfig(t, "invalid.toml")
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "toml")
}

func TestConfig_Load_InvalidRuntime(t *testing.T) {
	configContent := `[server]
registry_domain = "registry.example.com"
runtime = "invalid-runtime"`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	_, err = Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server.runtime must be one of")
}

func TestConfig_Load_InvalidRegistryDomain(t *testing.T) {
	testCases := []struct {
		name   string
		domain string
	}{
		{"with protocol", "https://registry.example.com"},
		{"with path", "registry.example.com/path"},
		{"with protocol and path", "https://registry.example.com/path"},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configContent := `[server]
registry_domain = "` + tc.domain + `"
runtime = "docker"

[registry_auth]
enabled = false`
			
			tempDir := t.TempDir()
			configFile := filepath.Join(tempDir, "gordon.toml")
			
			err := os.WriteFile(configFile, []byte(configContent), 0644)
			require.NoError(t, err)
			
			viper.Reset()
			viper.SetConfigFile(configFile)
			err = viper.ReadInConfig()
			require.NoError(t, err)
			
			_, err = Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "registry_domain should be just the domain name")
		})
	}
}

func TestConfig_Load_RegistryAuthValidation(t *testing.T) {
	configContent := `[server]
registry_domain = "registry.example.com"
runtime = "docker"

[registry_auth]
enabled = true
username = ""
password = ""`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	_, err = Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Registry auth enabled but username/password not provided")
}

func TestConfig_GetRoutes(t *testing.T) {
	cfg := &Config{
		Routes: map[string]string{
			"app.example.com":      "nginx:latest",
			"http://api.example.com": "api:v1",
			"secure.example.com":   "secure:latest",
		},
	}
	
	routes := cfg.GetRoutes()
	
	require.Len(t, routes, 3)
	
	// Find routes by domain
	var appRoute, apiRoute, secureRoute Route
	for _, route := range routes {
		switch route.Domain {
		case "app.example.com":
			appRoute = route
		case "api.example.com":
			apiRoute = route
		case "secure.example.com":
			secureRoute = route
		}
	}
	
	// Verify HTTPS defaults
	assert.Equal(t, "nginx:latest", appRoute.Image)
	assert.True(t, appRoute.HTTPS)
	
	// Verify HTTP override
	assert.Equal(t, "api:v1", apiRoute.Image)
	assert.False(t, apiRoute.HTTPS)
	
	// Verify HTTPS default
	assert.Equal(t, "secure:latest", secureRoute.Image)
	assert.True(t, secureRoute.HTTPS)
}

func TestExtractDomainFromImageName(t *testing.T) {
	testCases := []struct {
		name        string
		imageName   string
		expectedDomain string
		shouldMatch bool
	}{
		{"valid domain with tag", "app.example.com:latest", "app.example.com", true},
		{"valid domain without tag", "api.example.com", "api.example.com", true},
		{"subdomain", "staging.api.example.com:v1", "staging.api.example.com", true},
		{"simple name", "nginx", "", false},
		{"dockerhub format", "user/repo", "", false},
		{"localhost", "localhost:5000", "", false},
		{"ip address", "192.168.1.1:8080", "", false},
		{"invalid domain", "app-.example.com", "", false},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			domain, match := ExtractDomainFromImageName(tc.imageName)
			assert.Equal(t, tc.shouldMatch, match)
			assert.Equal(t, tc.expectedDomain, domain)
		})
	}
}

func TestConfig_AddRoute(t *testing.T) {
	// Create a config with existing routes
	configContent := `[server]
registry_domain = "registry.example.com"
runtime = "docker"

[registry_auth]
enabled = false

[routes]
"existing.example.com" = "existing:latest"`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	cfg, err := Load()
	require.NoError(t, err)
	
	// Add a new route
	err = cfg.AddRoute("new.example.com", "newapp:v1")
	require.NoError(t, err)
	
	// Verify route was added to memory
	assert.Equal(t, "newapp:v1", cfg.Routes["new.example.com"])
	
	// Verify config file was updated
	updatedContent, err := os.ReadFile(configFile)
	require.NoError(t, err)
	assert.Contains(t, string(updatedContent), `"new.example.com" = "newapp:v1"`)
	assert.Contains(t, string(updatedContent), `"existing.example.com" = "existing:latest"`)
}

func TestConfig_UpdateRoute(t *testing.T) {
	configContent := `[server]
registry_domain = "registry.example.com"
runtime = "docker"

[registry_auth]
enabled = false

[routes]
"app.example.com" = "oldapp:v1"`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	cfg, err := Load()
	require.NoError(t, err)
	
	// Update existing route
	err = cfg.UpdateRoute("app.example.com", "newapp:v2")
	require.NoError(t, err)
	
	// Verify route was updated in memory
	assert.Equal(t, "newapp:v2", cfg.Routes["app.example.com"])
	
	// Verify config file was updated
	updatedContent, err := os.ReadFile(configFile)
	require.NoError(t, err)
	assert.Contains(t, string(updatedContent), `"app.example.com" = "newapp:v2"`)
	assert.NotContains(t, string(updatedContent), "oldapp:v1")
}

func TestConfig_UpdateRoute_NonExistent(t *testing.T) {
	configContent := `[server]
registry_domain = "registry.example.com"
runtime = "docker"

[registry_auth]
enabled = false

[routes]`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	cfg, err := Load()
	require.NoError(t, err)
	
	// Try to update non-existent route
	err = cfg.UpdateRoute("nonexistent.example.com", "app:v1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "route nonexistent.example.com does not exist")
}

func TestValidateEnvConfig(t *testing.T) {
	testCases := []struct {
		name        string
		envConfig   EnvConfig
		expectError bool
		errorText   string
	}{
		{
			name: "valid config",
			envConfig: EnvConfig{
				Dir:       "/tmp/env",
				Providers: []string{"pass", "sops"},
			},
			expectError: false,
		},
		{
			name: "empty provider",
			envConfig: EnvConfig{
				Dir:       "/tmp/env",
				Providers: []string{"pass", "", "sops"},
			},
			expectError: true,
			errorText:   "empty provider name",
		},
		{
			name: "invalid provider",
			envConfig: EnvConfig{
				Dir:       "/tmp/env",
				Providers: []string{"pass", "invalid", "sops"},
			},
			expectError: true,
			errorText:   "unsupported provider 'invalid'",
		},
		{
			name: "non-existent directory file conflict",
			envConfig: EnvConfig{
				Dir:       "/dev/null", // This exists but is not a directory
				Providers: []string{"pass"},
			},
			expectError: true,
			errorText:   "not a directory",
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEnvConfig(&tc.envConfig)
			if tc.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errorText)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestGetDefaultDataDir(t *testing.T) {
	// Test should not fail regardless of environment
	dataDir := getDefaultDataDir()
	assert.NotEmpty(t, dataDir)
	
	// Should be either absolute path (for user home) or relative "./data"
	assert.True(t, filepath.IsAbs(dataDir) || dataDir == "./data")
}

func TestConfig_Load_NetworkIsolation(t *testing.T) {
	configContent := `[server]
registry_domain = "registry.example.com"
runtime = "docker"

[registry_auth]
enabled = false

[network_isolation]
enabled = true
network_prefix = "custom"
dns_suffix = ".local"`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	cfg, err := Load()
	require.NoError(t, err)
	
	assert.True(t, cfg.NetworkIsolation.Enabled)
	assert.Equal(t, "custom", cfg.NetworkIsolation.NetworkPrefix)
	assert.Equal(t, ".local", cfg.NetworkIsolation.DNSSuffix)
}

func TestConfig_Load_AttachmentsAndNetworkGroups(t *testing.T) {
	configContent := `[server]
registry_domain = "registry.example.com"
runtime = "docker"

[registry_auth]
enabled = false

[attachments]
"app.example.com" = ["postgres:14", "redis:6"]
"backend" = ["postgres:14"]

[network_groups]
"backend" = ["api.example.com", "worker.example.com"]`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	cfg, err := Load()
	require.NoError(t, err)
	
	// Verify attachments
	assert.Len(t, cfg.Attachments, 2)
	assert.Equal(t, []string{"postgres:14", "redis:6"}, cfg.Attachments["app.example.com"])
	assert.Equal(t, []string{"postgres:14"}, cfg.Attachments["backend"])
	
	// Verify network groups
	assert.Len(t, cfg.NetworkGroups, 1)
	assert.Equal(t, []string{"api.example.com", "worker.example.com"}, cfg.NetworkGroups["backend"])
}

func TestConfig_Load_EmptyConfiguration(t *testing.T) {
	// Test loading with completely empty config file but needs minimal valid config
	configContent := `[registry_auth]
enabled = false`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	cfg, err := Load()
	require.NoError(t, err)
	
	// Should have all defaults except what we explicitly set
	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 5000, cfg.Server.RegistryPort)
	assert.Equal(t, "auto", cfg.Server.Runtime)
	assert.False(t, cfg.RegistryAuth.Enabled) // explicitly disabled
	assert.True(t, cfg.Volumes.AutoCreate)
	assert.Equal(t, "gordon", cfg.Volumes.Prefix)
	assert.True(t, cfg.Volumes.Preserve)
	assert.True(t, cfg.NetworkIsolation.Enabled)
	assert.Equal(t, "gordon", cfg.NetworkIsolation.NetworkPrefix)
	assert.Equal(t, ".internal", cfg.NetworkIsolation.DNSSuffix)
}

func TestConfig_AddRoute_NoConfigFile(t *testing.T) {
	// Clear viper state to ensure no config file is set
	viper.Reset()
	
	cfg := &Config{
		Routes: make(map[string]string),
	}
	
	// Try to add route when no config file is available
	err := cfg.AddRoute("test.example.com", "test:latest")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no config file path available")
}

func TestConfig_AddRoute_FileReadError(t *testing.T) {
	// Create a config file in a non-existent directory
	configFile := "/nonexistent/directory/gordon.toml"
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	
	cfg := &Config{
		Routes: make(map[string]string),
	}
	
	err := cfg.AddRoute("test.example.com", "test:latest")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read config file")
}

func TestConfig_UpdateRoute_FileReadError(t *testing.T) {
	// Create a config file in a non-existent directory
	configFile := "/nonexistent/directory/gordon.toml"
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	
	cfg := &Config{
		Routes: map[string]string{
			"test.example.com": "test:v1",
		},
	}
	
	err := cfg.UpdateRoute("test.example.com", "test:v2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read config file")
}

func TestConfig_AddRoute_RouteAtEndOfFile(t *testing.T) {
	configContent := `[server]
registry_domain = "registry.example.com"
runtime = "docker"

[registry_auth]
enabled = false

[routes]
"existing.example.com" = "existing:latest"`
	
	tempDir := t.TempDir()
	configFile := filepath.Join(tempDir, "gordon.toml")
	
	err := os.WriteFile(configFile, []byte(configContent), 0644)
	require.NoError(t, err)
	
	viper.Reset()
	viper.SetConfigFile(configFile)
	err = viper.ReadInConfig()
	require.NoError(t, err)
	
	cfg, err := Load()
	require.NoError(t, err)
	
	// Add a new route at the end of the routes section
	err = cfg.AddRoute("end.example.com", "endapp:v1")
	require.NoError(t, err)
	
	// Verify route was added to memory
	assert.Equal(t, "endapp:v1", cfg.Routes["end.example.com"])
	
	// Verify config file was updated
	updatedContent, err := os.ReadFile(configFile)
	require.NoError(t, err)
	assert.Contains(t, string(updatedContent), `"end.example.com" = "endapp:v1"`)
}

func TestValidateEnvConfig_RelativePath(t *testing.T) {
	envConfig := EnvConfig{
		Dir:       "./relative/path", // relative path should not cause error
		Providers: []string{"pass"},
	}
	
	err := validateEnvConfig(&envConfig)
	require.NoError(t, err)
}

func TestValidateEnvConfig_EmptyDir(t *testing.T) {
	envConfig := EnvConfig{
		Dir:       "", // empty dir should not cause error  
		Providers: []string{"pass"},
	}
	
	err := validateEnvConfig(&envConfig)
	require.NoError(t, err)
}

func TestGetDefaultDataDir_Coverage(t *testing.T) {
	// This test mainly exists to ensure coverage of getDefaultDataDir branches
	// We can't easily test root vs non-root scenarios, but we can test the function
	dataDir := getDefaultDataDir()
	assert.NotEmpty(t, dataDir)
}