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
	"github.com/bnema/gordon/internal/domain"
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

	autoCreate, prefix, preserve := svc.GetVolumeConfig()
	assert.True(t, autoCreate)
	assert.Equal(t, "gordon", prefix)
	assert.False(t, preserve)
}

func TestService_Reload(t *testing.T) {
	t.Run("success - picks up config file changes", func(t *testing.T) {
		// Create temp config file
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		initialConfig := `[server]
port = 8080

[routes]
"app.example.com" = "myapp:v1"
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
		assert.Equal(t, "myapp:v1", svc.GetConfig().Routes["app.example.com"])

		// Modify config file on disk
		updatedConfig := `[server]
port = 9090

[routes]
"app.example.com" = "myapp:v2"
"new.example.com" = "newapp:latest"
`
		err = os.WriteFile(configFile, []byte(updatedConfig), 0600)
		require.NoError(t, err)

		// Reload should pick up new values
		err = svc.Reload(ctx)
		require.NoError(t, err)

		assert.Equal(t, 9090, svc.GetServerPort())
		assert.Equal(t, "myapp:v2", svc.GetConfig().Routes["app.example.com"])
		assert.Equal(t, "newapp:latest", svc.GetConfig().Routes["new.example.com"])
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
		"app.example.com": []interface{}{"redis:latest", "postgres:18"},
	})

	eventBus := mocks.NewMockEventPublisher(t)
	svc := NewService(v, eventBus)
	ctx := testContext()

	_ = svc.Load(ctx)

	attachments := svc.GetAttachments()

	assert.Len(t, attachments, 1)
	assert.ElementsMatch(t, []string{"redis:latest", "postgres:18"}, attachments["app.example.com"])
}

func TestService_GetAllAttachments(t *testing.T) {
	t.Run("returns all attachments", func(t *testing.T) {
		v := viper.New()
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest", "postgres:18"},
			"api.example.com": []interface{}{"rabbitmq:3"},
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		attachments := svc.GetAllAttachments(ctx)

		assert.Len(t, attachments, 2)
		assert.ElementsMatch(t, []string{"redis:latest", "postgres:18"}, attachments["app.example.com"])
		assert.ElementsMatch(t, []string{"rabbitmq:3"}, attachments["api.example.com"])
	})

	t.Run("returns empty map when no attachments", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		attachments := svc.GetAllAttachments(ctx)

		assert.Empty(t, attachments)
	})

	t.Run("returns copy not reference", func(t *testing.T) {
		v := viper.New()
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest"},
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		attachments := svc.GetAllAttachments(ctx)
		// Modify the returned map
		attachments["app.example.com"] = append(attachments["app.example.com"], "postgres:18")

		// Original should be unchanged
		original := svc.GetAllAttachments(ctx)
		assert.Len(t, original["app.example.com"], 1)
	})
}

func TestService_GetAttachmentsFor(t *testing.T) {
	t.Run("existing domain", func(t *testing.T) {
		v := viper.New()
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest", "postgres:18"},
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		images, err := svc.GetAttachmentsFor(ctx, "app.example.com")

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"redis:latest", "postgres:18"}, images)
	})

	t.Run("existing network group", func(t *testing.T) {
		v := viper.New()
		v.Set("attachments", map[string]interface{}{
			"backend": []interface{}{"rabbitmq:3", "redis:latest"},
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		images, err := svc.GetAttachmentsFor(ctx, "backend")

		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"rabbitmq:3", "redis:latest"}, images)
	})

	t.Run("non-existent target", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		images, err := svc.GetAttachmentsFor(ctx, "notfound.example.com")

		assert.ErrorIs(t, err, domain.ErrAttachmentNotFound)
		assert.Nil(t, images)
	})

	t.Run("returns copy not reference", func(t *testing.T) {
		v := viper.New()
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest"},
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		images, err := svc.GetAttachmentsFor(ctx, "app.example.com")
		require.NoError(t, err)

		// Modify the returned slice (use _ to satisfy linter)
		_ = append(images, "postgres:18")

		// Original should be unchanged
		original, _ := svc.GetAttachmentsFor(ctx, "app.example.com")
		assert.Len(t, original, 1)
	})
}

func TestService_AddAttachment(t *testing.T) {
	t.Run("success - new target", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		err := os.WriteFile(configFile, []byte("[attachments]\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err = svc.AddAttachment(ctx, "app.example.com", "postgres:18")

		assert.NoError(t, err)

		// Verify attachment was added
		config := svc.GetConfig()
		assert.Contains(t, config.Attachments["app.example.com"], "postgres:18")
	})

	t.Run("success - existing target", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		err := os.WriteFile(configFile, []byte("[attachments]\n\"app.example.com\" = [\"redis:latest\"]\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest"},
		})
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err = svc.AddAttachment(ctx, "app.example.com", "postgres:18")

		assert.NoError(t, err)

		// Verify attachment was added alongside existing
		config := svc.GetConfig()
		assert.ElementsMatch(t, []string{"redis:latest", "postgres:18"}, config.Attachments["app.example.com"])
	})

	t.Run("duplicate attachment", func(t *testing.T) {
		v := viper.New()
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest"},
		})
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err := svc.AddAttachment(ctx, "app.example.com", "redis:latest")

		assert.ErrorIs(t, err, domain.ErrAttachmentExists)
	})

	t.Run("empty target", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		err := svc.AddAttachment(ctx, "", "postgres:18")

		assert.ErrorIs(t, err, domain.ErrAttachmentTargetEmpty)
	})

	t.Run("empty image", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		err := svc.AddAttachment(ctx, "app.example.com", "")

		assert.ErrorIs(t, err, domain.ErrAttachmentImageEmpty)
	})

	t.Run("network group target", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		err := os.WriteFile(configFile, []byte("[attachments]\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err = svc.AddAttachment(ctx, "backend", "rabbitmq:3")

		assert.NoError(t, err)

		config := svc.GetConfig()
		assert.Contains(t, config.Attachments["backend"], "rabbitmq:3")
	})
}

func TestService_RemoveAttachment(t *testing.T) {
	t.Run("success - single attachment", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		err := os.WriteFile(configFile, []byte("[attachments]\n\"app.example.com\" = [\"redis:latest\"]\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest"},
		})
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err = svc.RemoveAttachment(ctx, "app.example.com", "redis:latest")

		assert.NoError(t, err)

		// Verify target was removed (no attachments left)
		config := svc.GetConfig()
		_, exists := config.Attachments["app.example.com"]
		assert.False(t, exists)
	})

	t.Run("success - multiple attachments", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		err := os.WriteFile(configFile, []byte("[attachments]\n\"app.example.com\" = [\"redis:latest\", \"postgres:18\"]\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest", "postgres:18"},
		})
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err = svc.RemoveAttachment(ctx, "app.example.com", "redis:latest")

		assert.NoError(t, err)

		// Verify only redis was removed
		config := svc.GetConfig()
		assert.Equal(t, []string{"postgres:18"}, config.Attachments["app.example.com"])
	})

	t.Run("target not found", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err := svc.RemoveAttachment(ctx, "notfound.example.com", "redis:latest")

		assert.ErrorIs(t, err, domain.ErrAttachmentNotFound)
	})

	t.Run("image not found in target", func(t *testing.T) {
		v := viper.New()
		v.Set("attachments", map[string]interface{}{
			"app.example.com": []interface{}{"redis:latest"},
		})
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err := svc.RemoveAttachment(ctx, "app.example.com", "postgres:18")

		assert.ErrorIs(t, err, domain.ErrAttachmentNotFound)
	})

	t.Run("empty target", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		err := svc.RemoveAttachment(ctx, "", "redis:latest")

		assert.ErrorIs(t, err, domain.ErrAttachmentTargetEmpty)
	})

	t.Run("empty image", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		err := svc.RemoveAttachment(ctx, "app.example.com", "")

		assert.ErrorIs(t, err, domain.ErrAttachmentImageEmpty)
	})

	t.Run("network group target", func(t *testing.T) {
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		err := os.WriteFile(configFile, []byte("[attachments]\n\"backend\" = [\"rabbitmq:3\"]\n"), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		v.Set("attachments", map[string]interface{}{
			"backend": []interface{}{"rabbitmq:3"},
		})
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		_ = svc.Load(ctx)

		err = svc.RemoveAttachment(ctx, "backend", "rabbitmq:3")

		assert.NoError(t, err)

		config := svc.GetConfig()
		_, exists := config.Attachments["backend"]
		assert.False(t, exists)
	})
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

func TestSplitImageNameTag(t *testing.T) {
	tests := []struct {
		name       string
		image      string
		wantName   string
		wantHasTag bool
	}{
		{"with tag", "myapp:latest", "myapp", true},
		{"with version tag", "myapp:v1.2.0", "myapp", true},
		{"no tag", "myapp", "myapp", false},
		{"empty string", "", "", false},
		{"tag only colon", "myapp:", "myapp", true},
		{"registry prefixed with tag", "reg.example.com/myapp:latest", "reg.example.com/myapp", true},
		{"registry prefixed no tag", "reg.example.com/myapp", "reg.example.com/myapp", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, hasTag := splitImageNameTag(tt.image)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantHasTag, hasTag)
		})
	}
}

func TestNormalizeRegistryImage(t *testing.T) {
	tests := []struct {
		name           string
		imageName      string
		registryDomain string
		expected       string
	}{
		{"strips registry prefix", "reg.example.com/myapp:latest", "reg.example.com", "myapp:latest"},
		{"no prefix to strip", "myapp:latest", "reg.example.com", "myapp:latest"},
		{"empty registry domain", "myapp:latest", "", "myapp:latest"},
		{"registry with trailing slash", "reg.example.com/myapp:latest", "reg.example.com/", "myapp:latest"},
		{"partial match is not stripped", "reg.example.com.evil/myapp:latest", "reg.example.com", "reg.example.com.evil/myapp:latest"},
		{"bare name no registry", "myapp", "reg.example.com", "myapp"},
		{"exact registry without image", "reg.example.com/", "reg.example.com", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NormalizeRegistryImage(tt.imageName, tt.registryDomain)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestService_FindRoutesByImage(t *testing.T) {
	t.Run("exact match with tag", func(t *testing.T) {
		v := viper.New()
		v.Set("routes", map[string]interface{}{
			"app.example.com": "myapp:latest",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp:latest")
		assert.Len(t, routes, 1)
		assert.Equal(t, "app.example.com", routes[0].Domain)
		assert.Equal(t, "myapp:latest", routes[0].Image)
	})

	t.Run("bare name matches route with tag", func(t *testing.T) {
		v := viper.New()
		v.Set("routes", map[string]interface{}{
			"app.example.com": "myapp:latest",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp")
		assert.Len(t, routes, 1)
		assert.Equal(t, "app.example.com", routes[0].Domain)
	})

	t.Run("bare name matches route with version tag", func(t *testing.T) {
		v := viper.New()
		v.Set("routes", map[string]interface{}{
			"app.example.com": "myapp:v2.0.0",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp")
		assert.Len(t, routes, 1)
		assert.Equal(t, "app.example.com", routes[0].Domain)
	})

	t.Run("tag mismatch does not match", func(t *testing.T) {
		v := viper.New()
		v.Set("routes", map[string]interface{}{
			"app.example.com": "myapp:v1",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp:v2")
		assert.Empty(t, routes)
	})

	t.Run("no match", func(t *testing.T) {
		v := viper.New()
		v.Set("routes", map[string]interface{}{
			"app.example.com": "myapp:latest",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "otherapp")
		assert.Empty(t, routes)
	})

	t.Run("strips registry prefix before matching", func(t *testing.T) {
		v := viper.New()
		v.Set("server.registry_domain", "reg.example.com")
		v.Set("routes", map[string]interface{}{
			"app.example.com": "reg.example.com/myapp:latest",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp")
		assert.Len(t, routes, 1)
		assert.Equal(t, "app.example.com", routes[0].Domain)
	})

	t.Run("strips registry prefix from input too", func(t *testing.T) {
		v := viper.New()
		v.Set("server.registry_domain", "reg.example.com")
		v.Set("routes", map[string]interface{}{
			"app.example.com": "reg.example.com/myapp:latest",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "reg.example.com/myapp:latest")
		assert.Len(t, routes, 1)
		assert.Equal(t, "app.example.com", routes[0].Domain)
	})

	t.Run("multiple routes for same image", func(t *testing.T) {
		v := viper.New()
		v.Set("routes", map[string]interface{}{
			"app1.example.com":  "myapp:latest",
			"app2.example.com":  "myapp:latest",
			"other.example.com": "otherapp:latest",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp")
		assert.Len(t, routes, 2)

		domains := []string{routes[0].Domain, routes[1].Domain}
		assert.ElementsMatch(t, []string{"app1.example.com", "app2.example.com"}, domains)
	})

	t.Run("case insensitive match", func(t *testing.T) {
		v := viper.New()
		v.Set("routes", map[string]interface{}{
			"app.example.com": "MyApp:latest",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp")
		assert.Len(t, routes, 1)
	})

	t.Run("http prefix route sets HTTPS false", func(t *testing.T) {
		v := viper.New()
		v.Set("routes", map[string]interface{}{
			"http://insecure.example.com": "myapp:latest",
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp")
		assert.Len(t, routes, 1)
		assert.Equal(t, "insecure.example.com", routes[0].Domain)
		assert.False(t, routes[0].HTTPS)
	})

	t.Run("empty routes", func(t *testing.T) {
		v := viper.New()
		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()
		_ = svc.Load(ctx)

		routes := svc.FindRoutesByImage(ctx, "myapp")
		assert.Empty(t, routes)
	})
}

func TestSavePreservesAllConfigFields(t *testing.T) {
	t.Run("network_groups are persisted to disk", func(t *testing.T) {
		// Setup: config file with auth, server, and network_groups
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		initialConfig := `
[server]
port = 9999

[auth]
username = "admin"
token_secret = "supersecretvalue"

[routes]
"app.example.com" = "myapp:latest"

[network_groups]
frontend = ["app1.example.com", "app2.example.com"]
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

		err = svc.Load(ctx)
		require.NoError(t, err)

		// Verify network_groups loaded correctly in memory
		groups := svc.GetNetworkGroups()
		require.Len(t, groups, 1)
		require.ElementsMatch(t, []string{"app1.example.com", "app2.example.com"}, groups["frontend"])

		// Trigger a Save via AddRoute (which calls Save internally)
		err = svc.AddRoute(ctx, domain.Route{Domain: "new.example.com", Image: "newapp:latest"})
		require.NoError(t, err)

		// Re-read the file to verify network_groups was persisted
		v2 := viper.New()
		v2.SetConfigFile(configFile)
		err = v2.ReadInConfig()
		require.NoError(t, err)

		// Verify network_groups is still in the file after save
		savedGroups := loadStringArrayMap(v2.Get("network_groups"))
		assert.Len(t, savedGroups, 1, "network_groups should still be present in saved config")
		assert.ElementsMatch(t, []string{"app1.example.com", "app2.example.com"}, savedGroups["frontend"])

		// Verify auth fields are preserved
		assert.Equal(t, "admin", v2.GetString("auth.username"))
		assert.Equal(t, "supersecretvalue", v2.GetString("auth.token_secret"))

		// Verify server fields are preserved
		assert.Equal(t, 9999, v2.GetInt("server.port"))
	})

	t.Run("network_groups added in memory are written to disk on Save", func(t *testing.T) {
		// Setup: config file WITHOUT network_groups
		tmpDir := t.TempDir()
		configFile := filepath.Join(tmpDir, "gordon.toml")
		initialConfig := `
[server]
port = 8080

[routes]
"app.example.com" = "myapp:latest"
`
		err := os.WriteFile(configFile, []byte(initialConfig), 0600)
		require.NoError(t, err)

		v := viper.New()
		v.SetConfigFile(configFile)
		err = v.ReadInConfig()
		require.NoError(t, err)

		// Inject network_groups into viper as if they were loaded from some other source
		// This simulates a case where network_groups exist in memory but not on disk
		v.Set("network_groups", map[string]interface{}{
			"backend": []interface{}{"api.example.com"},
		})

		eventBus := mocks.NewMockEventPublisher(t)
		svc := NewService(v, eventBus)
		ctx := testContext()

		err = svc.Load(ctx)
		require.NoError(t, err)

		// Verify network_groups is loaded in memory
		groups := svc.GetNetworkGroups()
		require.Len(t, groups, 1)

		// Trigger Save
		err = svc.AddRoute(ctx, domain.Route{Domain: "new.example.com", Image: "newapp:latest"})
		require.NoError(t, err)

		// Re-read the file
		v2 := viper.New()
		v2.SetConfigFile(configFile)
		err = v2.ReadInConfig()
		require.NoError(t, err)

		// network_groups must be present in the saved file
		savedGroups := loadStringArrayMap(v2.Get("network_groups"))
		assert.Len(t, savedGroups, 1, "network_groups added in memory must be written on Save")
		assert.ElementsMatch(t, []string{"api.example.com"}, savedGroups["backend"])
	})
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
