package container

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	inmocks "gordon/internal/boundaries/in/mocks"
	"gordon/internal/boundaries/out/mocks"
	"gordon/internal/domain"
)

// domainToEnvFileName tests

func TestDomainToEnvFileName(t *testing.T) {
	tests := []struct {
		name     string
		domain   string
		expected string
	}{
		{
			name:     "simple domain",
			domain:   "app.example.com",
			expected: "app_example_com.env",
		},
		{
			name:     "domain with port",
			domain:   "app.example.com:8080",
			expected: "app_example_com_8080.env",
		},
		{
			name:     "domain with path",
			domain:   "app.example.com/api",
			expected: "app_example_com_api.env",
		},
		{
			name:     "complex domain",
			domain:   "sub.app.example.com:443/v1",
			expected: "sub_app_example_com_443_v1.env",
		},
		{
			name:     "single word",
			domain:   "localhost",
			expected: "localhost.env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := domainToEnvFileName(tt.domain)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// parseEnvFile tests

func TestParseEnvFile(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected map[string]string
		wantErr  bool
	}{
		{
			name:  "simple key-value pairs",
			input: "FOO=bar\nBAZ=qux",
			expected: map[string]string{
				"FOO": "bar",
				"BAZ": "qux",
			},
		},
		{
			name:  "with comments and empty lines",
			input: "# This is a comment\nFOO=bar\n\n# Another comment\nBAZ=qux\n",
			expected: map[string]string{
				"FOO": "bar",
				"BAZ": "qux",
			},
		},
		{
			name:  "with quoted values - double quotes",
			input: `FOO="bar baz"`,
			expected: map[string]string{
				"FOO": "bar baz",
			},
		},
		{
			name:  "with quoted values - single quotes",
			input: `FOO='bar baz'`,
			expected: map[string]string{
				"FOO": "bar baz",
			},
		},
		{
			name:  "value with equals sign",
			input: "DATABASE_URL=postgres://user:pass@host/db?option=value",
			expected: map[string]string{
				"DATABASE_URL": "postgres://user:pass@host/db?option=value",
			},
		},
		{
			name:  "empty value",
			input: "EMPTY=",
			expected: map[string]string{
				"EMPTY": "",
			},
		},
		{
			name:     "empty file",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:     "only comments",
			input:    "# comment 1\n# comment 2",
			expected: map[string]string{},
		},
		{
			name:     "malformed line without equals",
			input:    "MALFORMED\nGOOD=value",
			expected: map[string]string{"GOOD": "value"},
		},
		{
			name:  "whitespace trimming",
			input: "  FOO  =  bar  \n  BAZ=qux",
			expected: map[string]string{
				"FOO": "bar",
				"BAZ": "qux",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseEnvFile([]byte(tt.input))

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// parseConfigDigest tests

func TestParseConfigDigest(t *testing.T) {
	tests := []struct {
		name     string
		manifest map[string]any
		expected string
		wantErr  bool
	}{
		{
			name: "valid manifest v2",
			manifest: map[string]any{
				"schemaVersion": 2,
				"mediaType":     "application/vnd.docker.distribution.manifest.v2+json",
				"config": map[string]any{
					"mediaType": "application/vnd.docker.container.image.v1+json",
					"digest":    "sha256:abc123",
					"size":      1234,
				},
			},
			expected: "sha256:abc123",
		},
		{
			name: "OCI manifest",
			manifest: map[string]any{
				"schemaVersion": 2,
				"mediaType":     "application/vnd.oci.image.manifest.v1+json",
				"config": map[string]any{
					"mediaType": "application/vnd.oci.image.config.v1+json",
					"digest":    "sha256:def456",
					"size":      5678,
				},
			},
			expected: "sha256:def456",
		},
		{
			name: "empty config digest",
			manifest: map[string]any{
				"schemaVersion": 2,
				"config": map[string]any{
					"digest": "",
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.manifest)
			require.NoError(t, err)

			result, err := parseConfigDigest(data)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseConfigDigest_InvalidJSON(t *testing.T) {
	_, err := parseConfigDigest([]byte("invalid json"))
	assert.Error(t, err)
}

// parseImageLabels tests

func TestParseImageLabels(t *testing.T) {
	tests := []struct {
		name     string
		config   map[string]any
		expected *domain.ImageLabels
	}{
		{
			name: "single domain label",
			config: map[string]any{
				"config": map[string]any{
					"Labels": map[string]any{
						"gordon.domain": "app.example.com",
					},
				},
			},
			expected: &domain.ImageLabels{
				Domain: "app.example.com",
			},
		},
		{
			name: "multiple domains label",
			config: map[string]any{
				"config": map[string]any{
					"Labels": map[string]any{
						"gordon.domains": "app1.example.com, app2.example.com",
					},
				},
			},
			expected: &domain.ImageLabels{
				Domains: []string{"app1.example.com", "app2.example.com"},
			},
		},
		{
			name: "all gordon labels",
			config: map[string]any{
				"config": map[string]any{
					"Labels": map[string]any{
						"gordon.domain":   "app.example.com",
						"gordon.health":   "/healthz",
						"gordon.port":     "8080",
						"gordon.env-file": ".env.prod",
					},
				},
			},
			expected: &domain.ImageLabels{
				Domain:  "app.example.com",
				Health:  "/healthz",
				Port:    "8080",
				EnvFile: ".env.prod",
			},
		},
		{
			name: "no labels",
			config: map[string]any{
				"config": map[string]any{
					"Labels": nil,
				},
			},
			expected: &domain.ImageLabels{},
		},
		{
			name: "no gordon labels",
			config: map[string]any{
				"config": map[string]any{
					"Labels": map[string]any{
						"other.label": "value",
					},
				},
			},
			expected: &domain.ImageLabels{},
		},
		{
			name: "whitespace in labels",
			config: map[string]any{
				"config": map[string]any{
					"Labels": map[string]any{
						"gordon.domain": "  app.example.com  ",
					},
				},
			},
			expected: &domain.ImageLabels{
				Domain: "app.example.com",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.config)
			require.NoError(t, err)

			result, err := parseImageLabels(data)
			require.NoError(t, err)

			assert.Equal(t, tt.expected.Domain, result.Domain)
			assert.Equal(t, tt.expected.Domains, result.Domains)
			assert.Equal(t, tt.expected.Health, result.Health)
			assert.Equal(t, tt.expected.Port, result.Port)
			assert.Equal(t, tt.expected.EnvFile, result.EnvFile)
		})
	}
}

func TestParseImageLabels_InvalidJSON(t *testing.T) {
	_, err := parseImageLabels([]byte("invalid json"))
	assert.Error(t, err)
}

// AutoRouteHandler tests

func TestAutoRouteHandler_CanHandle(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	assert.True(t, handler.CanHandle(domain.EventImagePushed))
	assert.False(t, handler.CanHandle(domain.EventConfigReload))
	assert.False(t, handler.CanHandle(domain.EventManualReload))
}

func TestAutoRouteHandler_Handle_DisabledAutoRoute(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	configSvc.EXPECT().IsAutoRouteEnabled().Return(false)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{
			Name:      "myapp",
			Reference: "latest",
			Manifest:  []byte(`{"schemaVersion": 2}`),
		},
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestAutoRouteHandler_Handle_InvalidPayload(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	configSvc.EXPECT().IsAutoRouteEnabled().Return(true)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventImagePushed,
		Data: "invalid payload type",
	}

	err := handler.Handle(event)

	// Handler skips invalid payload, doesn't error
	assert.NoError(t, err)
}

func TestAutoRouteHandler_Handle_EmptyManifest(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	configSvc.EXPECT().IsAutoRouteEnabled().Return(true)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{
			Name:      "myapp",
			Reference: "latest",
			Manifest:  []byte{},
		},
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestAutoRouteHandler_Handle_CreatesNewRoute(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	// Build manifest with config digest
	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"digest": "sha256:configdigest123",
		},
	}
	manifestData, _ := json.Marshal(manifest)

	// Build image config with gordon labels
	imageConfig := map[string]any{
		"config": map[string]any{
			"Labels": map[string]any{
				"gordon.domain": "app.example.com",
			},
		},
	}
	configData, _ := json.Marshal(imageConfig)

	configSvc.EXPECT().IsAutoRouteEnabled().Return(true)
	blobStorage.EXPECT().GetBlob("sha256:configdigest123").Return(io.NopCloser(strings.NewReader(string(configData))), nil)

	// No existing routes
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{})

	// Expect route to be added
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:latest",
	}).Return(nil)

	// Expect deploy to be triggered
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:latest",
	}).Return(&domain.Container{ID: "container-1"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{
			Name:      "myapp",
			Reference: "latest",
			Manifest:  manifestData,
		},
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestAutoRouteHandler_Handle_UpdatesExistingRoute(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	// Build manifest with config digest
	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"digest": "sha256:configdigest123",
		},
	}
	manifestData, _ := json.Marshal(manifest)

	// Build image config with gordon labels
	imageConfig := map[string]any{
		"config": map[string]any{
			"Labels": map[string]any{
				"gordon.domain": "app.example.com",
			},
		},
	}
	configData, _ := json.Marshal(imageConfig)

	configSvc.EXPECT().IsAutoRouteEnabled().Return(true)
	blobStorage.EXPECT().GetBlob("sha256:configdigest123").Return(io.NopCloser(strings.NewReader(string(configData))), nil)

	// Existing route with different image
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "myapp:v1"},
	})

	// Expect route to be updated
	configSvc.EXPECT().UpdateRoute(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:v2",
	}).Return(nil)

	// Expect deploy to be triggered
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:v2",
	}).Return(&domain.Container{ID: "container-1"}, nil)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{
			Name:      "myapp",
			Reference: "v2",
			Manifest:  manifestData,
		},
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestAutoRouteHandler_Handle_NoUpdateIfSameImage(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	// Build manifest with config digest
	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"digest": "sha256:configdigest123",
		},
	}
	manifestData, _ := json.Marshal(manifest)

	// Build image config with gordon labels
	imageConfig := map[string]any{
		"config": map[string]any{
			"Labels": map[string]any{
				"gordon.domain": "app.example.com",
			},
		},
	}
	configData, _ := json.Marshal(imageConfig)

	configSvc.EXPECT().IsAutoRouteEnabled().Return(true)
	blobStorage.EXPECT().GetBlob("sha256:configdigest123").Return(io.NopCloser(strings.NewReader(string(configData))), nil)

	// Existing route with same image - no update needed
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "myapp:latest"},
	})

	// No AddRoute, UpdateRoute, or Deploy calls expected

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{
			Name:      "myapp",
			Reference: "latest",
			Manifest:  manifestData,
		},
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestAutoRouteHandler_Handle_NoDomainLabel(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	// Build manifest with config digest
	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"digest": "sha256:configdigest123",
		},
	}
	manifestData, _ := json.Marshal(manifest)

	// Build image config with no gordon labels
	imageConfig := map[string]any{
		"config": map[string]any{
			"Labels": map[string]any{
				"other.label": "value",
			},
		},
	}
	configData, _ := json.Marshal(imageConfig)

	configSvc.EXPECT().IsAutoRouteEnabled().Return(true)
	blobStorage.EXPECT().GetBlob("sha256:configdigest123").Return(io.NopCloser(strings.NewReader(string(configData))), nil)

	// No route operations expected

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{
			Name:      "myapp",
			Reference: "latest",
			Manifest:  manifestData,
		},
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestAutoRouteHandler_Handle_MultipleDomains(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	// Build manifest with config digest
	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"digest": "sha256:configdigest123",
		},
	}
	manifestData, _ := json.Marshal(manifest)

	// Build image config with multiple domains
	imageConfig := map[string]any{
		"config": map[string]any{
			"Labels": map[string]any{
				"gordon.domain":  "app.example.com",
				"gordon.domains": "api.example.com, www.example.com",
			},
		},
	}
	configData, _ := json.Marshal(imageConfig)

	configSvc.EXPECT().IsAutoRouteEnabled().Return(true)
	blobStorage.EXPECT().GetBlob("sha256:configdigest123").Return(io.NopCloser(strings.NewReader(string(configData))), nil)

	// No existing routes
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{}).Times(3)

	// Expect routes to be added for all three domains
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:latest",
	}).Return(nil)
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{
		Domain: "api.example.com",
		Image:  "myapp:latest",
	}).Return(nil)
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{
		Domain: "www.example.com",
		Image:  "myapp:latest",
	}).Return(nil)

	// Expect deploy for all three
	containerSvc.EXPECT().Deploy(mock.Anything, mock.AnythingOfType("domain.Route")).Return(&domain.Container{ID: "container-1"}, nil).Times(3)

	event := domain.Event{
		ID:   "event-123",
		Type: domain.EventImagePushed,
		Data: domain.ImagePushedPayload{
			Name:      "myapp",
			Reference: "latest",
			Manifest:  manifestData,
		},
	}

	err := handler.Handle(event)

	assert.NoError(t, err)
}

func TestAutoRouteHandler_TriggerDeploy_NilContainerService(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	// Create handler without container service
	handler := NewAutoRouteHandler(ctx, configSvc, nil, blobStorage, "registry.example.com")

	route := domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:latest",
	}

	// Should not panic, just log and skip
	handler.triggerDeploy(ctx, route)
}

// collectDomains tests

func TestAutoRouteHandler_CollectDomains(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	tests := []struct {
		name     string
		labels   *domain.ImageLabels
		expected []string
	}{
		{
			name: "single domain",
			labels: &domain.ImageLabels{
				Domain: "app.example.com",
			},
			expected: []string{"app.example.com"},
		},
		{
			name: "domains list only",
			labels: &domain.ImageLabels{
				Domains: []string{"api.example.com", "www.example.com"},
			},
			expected: []string{"api.example.com", "www.example.com"},
		},
		{
			name: "both domain and domains",
			labels: &domain.ImageLabels{
				Domain:  "app.example.com",
				Domains: []string{"api.example.com"},
			},
			expected: []string{"app.example.com", "api.example.com"},
		},
		{
			name:     "empty labels",
			labels:   &domain.ImageLabels{},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.collectDomains(tt.labels)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// buildImageName tests

func TestAutoRouteHandler_BuildImageName(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	tests := []struct {
		name      string
		imageName string
		reference string
		expected  string
	}{
		{
			name:      "with tag",
			imageName: "myapp",
			reference: "latest",
			expected:  "myapp:latest",
		},
		{
			name:      "with version tag",
			imageName: "myapp",
			reference: "v1.2.3",
			expected:  "myapp:v1.2.3",
		},
		{
			name:      "empty reference",
			imageName: "myapp",
			reference: "",
			expected:  "myapp",
		},
		{
			name:      "with digest",
			imageName: "myapp",
			reference: "sha256:abc123",
			expected:  "myapp:sha256:abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.buildImageName(tt.imageName, tt.reference)
			assert.Equal(t, tt.expected, result)
		})
	}
}
