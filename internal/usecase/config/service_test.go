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

	"gordon/internal/boundaries/out/mocks"
	"gordon/internal/domain"
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
	v.Set("registry_auth.enabled", true)
	v.Set("registry_auth.username", "admin")
	v.Set("registry_auth.password", "secret")
	v.Set("volumes.auto_create", true)
	v.Set("volumes.prefix", "gordon")
	v.Set("volumes.preserve", false)
	v.Set("routes", map[string]interface{}{
		"app.example.com": "myapp:latest",
	})

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	err := svc.Load(ctx)

	assert.NoError(t, err)
	assert.Equal(t, 8080, svc.GetServerPort())
	assert.Equal(t, 5000, svc.GetRegistryPort())
	assert.Equal(t, "registry.example.com", svc.GetRegistryDomain())
	assert.Equal(t, "/var/gordon", svc.GetDataDir())
	assert.True(t, svc.IsAutoRouteEnabled())
	assert.True(t, svc.IsNetworkIsolationEnabled())
	assert.Equal(t, "gordon", svc.GetNetworkPrefix())

	enabled, username, password := svc.GetRegistryAuthConfig()
	assert.True(t, enabled)
	assert.Equal(t, "admin", username)
	assert.Equal(t, "secret", password)

	autoCreate, prefix, preserve := svc.GetVolumeConfig()
	assert.True(t, autoCreate)
	assert.Equal(t, "gordon", prefix)
	assert.False(t, preserve)
}

func TestService_GetRoutes(t *testing.T) {
	v := viper.New()
	v.Set("routes", map[string]interface{}{
		"app1.example.com":        "myapp:latest",
		"app2.example.com":        "otherapp:v2",
		"http://insecure.example": "insecureapp:latest",
	})

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	routes := svc.GetRoutes(ctx)

	assert.Len(t, routes, 3)

	// Check routes (order is not guaranteed)
	routeMap := make(map[string]domain.Route)
	for _, r := range routes {
		routeMap[r.Domain] = r
	}

	assert.Equal(t, "myapp:latest", routeMap["app1.example.com"].Image)
	assert.True(t, routeMap["app1.example.com"].HTTPS)

	assert.Equal(t, "otherapp:v2", routeMap["app2.example.com"].Image)
	assert.True(t, routeMap["app2.example.com"].HTTPS)

	assert.Equal(t, "insecureapp:latest", routeMap["insecure.example"].Image)
	assert.False(t, routeMap["insecure.example"].HTTPS)
}

func TestService_GetRoute(t *testing.T) {
	v := viper.New()
	v.Set("routes", map[string]interface{}{
		"app.example.com":         "myapp:latest",
		"http://insecure.example": "insecureapp:latest",
	})

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	t.Run("existing route", func(t *testing.T) {
		route, err := svc.GetRoute(ctx, "app.example.com")
		require.NoError(t, err)
		assert.Equal(t, "app.example.com", route.Domain)
		assert.Equal(t, "myapp:latest", route.Image)
		assert.True(t, route.HTTPS)
	})

	t.Run("non-existent route", func(t *testing.T) {
		route, err := svc.GetRoute(ctx, "notfound.example.com")
		assert.ErrorIs(t, err, domain.ErrRouteNotFound)
		assert.Nil(t, route)
	})
}

func TestService_AddRoute(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Create temp config file for Save() to work
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		err := os.WriteFile(configFile, []byte("[routes]\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		route := domain.Route{
			Domain: "new.example.com",
			Image:  "newapp:latest",
		}

		err = svc.AddRoute(ctx, route)

		assert.NoError(t, err)

		// Verify route was added
		config := svc.GetConfig()
		assert.Equal(t, "newapp:latest", config.Routes["new.example.com"])
	})

	t.Run("empty domain", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		route := domain.Route{
			Domain: "",
			Image:  "myapp:latest",
		}

		err := svc.AddRoute(ctx, route)
		assert.ErrorIs(t, err, domain.ErrRouteDomainEmpty)
	})

	t.Run("empty image", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		route := domain.Route{
			Domain: "example.com",
			Image:  "",
		}

		err := svc.AddRoute(ctx, route)
		assert.ErrorIs(t, err, domain.ErrRouteImageEmpty)
	})
}

func TestService_UpdateRoute(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Create temp config file for Save() to work
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		err := os.WriteFile(configFile, []byte("[routes]\n\"app.example.com\" = \"myapp:v1\"\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		v.Set("routes", map[string]any{
			"app.example.com": "myapp:v1",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		route := domain.Route{
			Domain: "app.example.com",
			Image:  "myapp:v2",
		}

		err = svc.UpdateRoute(ctx, route)

		assert.NoError(t, err)

		config := svc.GetConfig()
		assert.Equal(t, "myapp:v2", config.Routes["app.example.com"])
	})

	t.Run("not found", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		route := domain.Route{
			Domain: "nonexistent.example.com",
			Image:  "myapp:latest",
		}

		err := svc.UpdateRoute(ctx, route)
		assert.ErrorIs(t, err, domain.ErrRouteNotFound)
	})

	t.Run("empty domain", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		route := domain.Route{
			Domain: "",
			Image:  "myapp:latest",
		}

		err := svc.UpdateRoute(ctx, route)
		assert.ErrorIs(t, err, domain.ErrRouteDomainEmpty)
	})

	t.Run("empty image", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		route := domain.Route{
			Domain: "example.com",
			Image:  "",
		}

		err := svc.UpdateRoute(ctx, route)
		assert.ErrorIs(t, err, domain.ErrRouteImageEmpty)
	})
}

func TestService_RemoveRoute(t *testing.T) {
	// Create temp config file for Save() to work
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "gordon.toml")
	err := os.WriteFile(configFile, []byte("[routes]\n\"app.example.com\" = \"myapp:latest\"\n"), 0600)
	require.NoError(t, err)

	v := viper.New()
	v.SetConfigFile(configFile)
	v.Set("routes", map[string]interface{}{
		"app.example.com": "myapp:latest",
	})

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	err = svc.RemoveRoute(ctx, "app.example.com")

	assert.NoError(t, err)

	config := svc.GetConfig()
	_, exists := config.Routes["app.example.com"]
	assert.False(t, exists)
}

func TestService_RemoveRoute_NotFound(t *testing.T) {
	v := viper.New()
	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	err := svc.RemoveRoute(ctx, "nonexistent.example.com")

	assert.ErrorIs(t, err, domain.ErrRouteNotFound)
}

func TestService_GetNetworkGroups(t *testing.T) {
	v := viper.New()
	v.Set("network_groups", map[string]interface{}{
		"frontend": []interface{}{"app1.example.com", "app2.example.com"},
		"backend":  []interface{}{"api.example.com", "db.example.com"},
	})

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	groups := svc.GetNetworkGroups()

	assert.Len(t, groups, 2)
	assert.ElementsMatch(t, []string{"app1.example.com", "app2.example.com"}, groups["frontend"])
	assert.ElementsMatch(t, []string{"api.example.com", "db.example.com"}, groups["backend"])
}

func TestService_GetAttachments(t *testing.T) {
	v := viper.New()
	v.Set("attachments", map[string]interface{}{
		"app.example.com": []interface{}{"redis:latest", "postgres:15"},
	})

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	attachments := svc.GetAttachments()

	assert.Len(t, attachments, 1)
	assert.ElementsMatch(t, []string{"redis:latest", "postgres:15"}, attachments["app.example.com"])
}

func TestService_GetExternalRoutes(t *testing.T) {
	v := viper.New()
	v.Set("external_routes", map[string]interface{}{
		"reg.example.com":   "localhost:5000",
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

func TestLoadStringArrayMap(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected map[string][]string
	}{
		{
			name:     "nil input",
			input:    nil,
			expected: map[string][]string{},
		},
		{
			name: "valid map",
			input: map[string]any{
				"group1": []any{"a", "b"},
				"group2": []any{"c", "d", "e"},
			},
			expected: map[string][]string{
				"group1": {"a", "b"},
				"group2": {"c", "d", "e"},
			},
		},
		{
			name:     "invalid type",
			input:    "not a map",
			expected: map[string][]string{},
		},
		{
			name: "non-array value",
			input: map[string]any{
				"array":  []any{"a", "b"},
				"string": "not an array",
			},
			expected: map[string][]string{
				"array": {"a", "b"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := loadStringArrayMap(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
