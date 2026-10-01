package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
)

func testContext() context.Context {
	return zerowrap.WithCtx(context.Background(), zerowrap.Default())
}

func TestService_Load(t *testing.T) {
	v := viper.New()
	v.Set("server.port", 8080)
	v.Set("server.registry_port", 5000)
	v.Set("server.registry_domain", "registry.example.com")
	v.Set("server.data_dir", "/var/gordon")
	v.Set("auto_route.enabled", true)
	v.Set("network_isolation.enabled", true)
	v.Set("network_isolation.network_prefix", "gordon")
	v.Set("auth.enabled", true)
	v.Set("auth.username", "admin")
	v.Set("auth.password", "secret")
	v.Set("volumes.auto_create", true)
	v.Set("volumes.prefix", "gordon")
	v.Set("volumes.preserve", false)

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	err := svc.Load(ctx)

	assert.NoError(t, err)
	assert.Equal(t, 8080, svc.GetServerPort())
	assert.Equal(t, 5000, svc.GetRegistryPort())
	assert.Equal(t, "registry.example.com", svc.GetRegistryDomain())
	assert.Equal(t, "/var/gordon", svc.GetDataDir())
	assert.True(t, svc.IsNetworkIsolationEnabled())
	assert.Equal(t, "gordon", svc.GetNetworkPrefix())

	autoCreate, prefix, preserve := svc.GetVolumeConfig()
	assert.True(t, autoCreate)
	assert.Equal(t, "gordon", prefix)
	assert.False(t, preserve)
}

func TestService_LoadPrefersExplicitRegistryAddress(t *testing.T) {
	v := viper.New()
	v.Set("server.gordon_domain", "gordon.example.com")
	v.Set("server.registry_domain", "100.64.0.10:15000")

	svc := NewService(v, mocks.NewMockEventPublisher(t))
	require.NoError(t, svc.Load(testContext()))
	assert.Equal(t, "100.64.0.10:15000", svc.GetRegistryDomain())
}

func TestService_Reload(t *testing.T) {
	t.Run("success - picks up config file changes", func(t *testing.T) {
		// Create temp config file
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		initialConfig := `[server]
port = 8080
`
		err := os.WriteFile(configFile, []byte(initialConfig), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		err = v.ReadInConfig()
		require.NoError(t, err)

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		// Initial load
		err = svc.Load(ctx)
		require.NoError(t, err)
		assert.Equal(t, 8080, svc.GetServerPort())

		// Modify config file on disk
		updatedConfig := `[server]
port = 9090
`
		err = os.WriteFile(configFile, []byte(updatedConfig), 0600)
		require.NoError(t, err)

		// Reload should pick up new values
		err = svc.Reload(ctx)
		require.NoError(t, err)

		assert.Equal(t, 9090, svc.GetServerPort())
	})

	t.Run("error - config file not found", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")

		// Create config, load it, then delete it
		err := os.WriteFile(configFile, []byte("[server]\nport = 8080\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		err = v.ReadInConfig()
		require.NoError(t, err)

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		err = svc.Load(ctx)
		require.NoError(t, err)

		// Delete the config file
		err = os.Remove(configFile)
		require.NoError(t, err)

		// Reload should fail
		err = svc.Reload(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read config file")
	})

	t.Run("error - invalid config syntax", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")

		// Create valid config initially
		err := os.WriteFile(configFile, []byte("[server]\nport = 8080\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		err = v.ReadInConfig()
		require.NoError(t, err)

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		err = svc.Load(ctx)
		require.NoError(t, err)

		// Write invalid TOML
		err = os.WriteFile(configFile, []byte("[server\nport = invalid syntax"), 0600)
		require.NoError(t, err)

		// Reload should fail
		err = svc.Reload(ctx)
		assert.Error(t, err)
	})
}

func TestLoadExternalRoutes_RejectsInvalidAndDuplicateKeys(t *testing.T) {
	t.Run("invalid", func(t *testing.T) {
		_, err := loadExternalRoutes(map[string]any{
			"bad.example.com:8080": "203.0.113.10:5000",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid external route key")
	})

	t.Run("duplicate canonical", func(t *testing.T) {
		_, err := loadExternalRoutes(map[string]any{
			"Reg.Example.com": "203.0.113.10:5000",
			"reg.example.com": "203.0.113.11:5000",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate external route key")
	})
}

func TestService_GetExternalRoutes(t *testing.T) {
	v := viper.New()
	v.Set("external_routes", map[string]interface{}{
		"Reg.Example.com":   "localhost:5000",
		"cache.example.com": "127.0.0.1:6379",
	})

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	routes := svc.GetExternalRoutes()

	assert.Len(t, routes, 2)
	assert.Equal(t, "localhost:5000", routes["reg.example.com"])
	assert.Equal(t, "127.0.0.1:6379", routes["cache.example.com"])
}

func TestService_GetExternalRoutes_Empty(t *testing.T) {
	v := viper.New()

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	routes := svc.GetExternalRoutes()

	assert.Empty(t, routes)
}

func TestExtractDomainFromImageName(t *testing.T) {
	tests := []struct {
		name          string
		imageName     string
		expectedDom   string
		expectedFound bool
	}{
		{
			name:          "domain like image",
			imageName:     "myapp.example.com:latest",
			expectedDom:   "myapp.example.com",
			expectedFound: true,
		},
		{
			name:          "domain like image without tag",
			imageName:     "api.backend.io",
			expectedDom:   "api.backend.io",
			expectedFound: true,
		},
		{
			name:          "simple image name",
			imageName:     "nginx:latest",
			expectedDom:   "",
			expectedFound: false,
		},
		{
			name:          "registry path",
			imageName:     "gcr.io/project/image:tag",
			expectedDom:   "gcr.io/project/image",
			expectedFound: true,
		},
		{
			name:          "leading dot",
			imageName:     ".invalid:latest",
			expectedDom:   "",
			expectedFound: false,
		},
		{
			name:          "trailing dot",
			imageName:     "invalid.:latest",
			expectedDom:   "",
			expectedFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			domain, found := ExtractDomainFromImageName(tt.imageName)
			assert.Equal(t, tt.expectedDom, domain)
			assert.Equal(t, tt.expectedFound, found)
		})
	}
}

func TestLoadStringMap(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected map[string]string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: map[string]string{},
		},
		{
			name: "valid map",
			input: map[string]any{
				"key1": "value1",
				"key2": "value2",
			},
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:     "invalid type",
			input:    "not a map",
			expected: map[string]string{},
		},
		{
			name: "mixed values",
			input: map[string]any{
				"string": "value",
				"int":    123,
			},
			expected: map[string]string{
				"string": "value",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := loadStringMap(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
