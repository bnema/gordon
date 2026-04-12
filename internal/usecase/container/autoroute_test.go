package container

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/auto"
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
			expected: "YXBwLmV4YW1wbGUuY29t.env",
		},
		{
			name:     "domain with port",
			domain:   "app.example.com:8080",
			expected: "YXBwLmV4YW1wbGUuY29tOjgwODA.env",
		},
		{
			name:     "domain with path",
			domain:   "app.example.com/api",
			expected: "YXBwLmV4YW1wbGUuY29tL2FwaQ.env",
		},
		{
			name:     "complex domain",
			domain:   "sub.app.example.com:443/v1",
			expected: "c3ViLmFwcC5leGFtcGxlLmNvbTo0NDMvdjE.env",
		},
		{
			name:     "single word",
			domain:   "localhost",
			expected: "bG9jYWxob3N0.env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := domainToEnvFileName(tt.domain)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ParseEnvData tests

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
			result, err := domain.ParseEnvData([]byte(tt.input))

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// parseConfigDigest tests — now delegated to auto package; tested there directly.
// These tests call auto.ParseConfigDigest to maintain coverage for this package.

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

			result, err := auto.ParseConfigDigest(data)

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
	_, err := auto.ParseConfigDigest([]byte("invalid json"))
	assert.Error(t, err)
}

// parseImageLabels tests — now delegated to auto package; tested there directly.

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
						"gordon.domain":     "app.example.com",
						"gordon.health":     "/healthz",
						"gordon.proxy.port": "8080",
						"gordon.env-file":   ".env.prod",
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

			result, err := auto.ParseImageLabels(data)
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
	_, err := auto.ParseImageLabels([]byte("invalid json"))
	assert.Error(t, err)
}

func TestMatchesDomainAllowlist(t *testing.T) {
	tests := []struct {
		name     string
		domain   string
		patterns []string
		want     bool
	}{
		{name: "exact match", domain: "app.example.com", patterns: []string{"app.example.com"}, want: true},
		{name: "no match", domain: "app.example.com", patterns: []string{"api.example.com"}, want: false},
		{name: "wildcard match", domain: "app.example.com", patterns: []string{"*.example.com"}, want: true},
		{name: "wildcard no match on root", domain: "example.com", patterns: []string{"*.example.com"}, want: false},
		{name: "wildcard matches one level only", domain: "api.app.example.com", patterns: []string{"*.example.com"}, want: false},
		{name: "empty allowlist", domain: "app.example.com", patterns: nil, want: false},
		{name: "case insensitive", domain: "App.Example.Com", patterns: []string{"APP.EXAMPLE.COM"}, want: true},
		{name: "multiple patterns", domain: "api.example.com", patterns: []string{"foo.com", "*.example.com"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, auto.MatchesDomainAllowlist(tt.domain, tt.patterns))
		})
	}
}

func TestExtractRepoName(t *testing.T) {
	tests := []struct {
		name           string
		imageRef       string
		registryDomain string
		want           string
	}{
		{name: "simple tag", imageRef: "myapp:latest", want: "myapp"},
		{name: "version tag", imageRef: "myapp:v2.0.0", want: "myapp"},
		{name: "digest", imageRef: "myapp@sha256:abc123", want: "myapp"},
		{name: "strip registry", imageRef: "registry.example.com/myapp:latest", registryDomain: "registry.example.com", want: "myapp"},
		{name: "org image", imageRef: "org/myapp:latest", want: "org/myapp"},
		{name: "registry org image", imageRef: "registry.example.com/org/myapp:latest", registryDomain: "registry.example.com", want: "org/myapp"},
		{name: "lowercase", imageRef: "MyApp:Latest", want: "myapp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, auto.ExtractRepoName(tt.imageRef, tt.registryDomain))
		})
	}
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

	err := handler.Handle(context.Background(), event)

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

	err := handler.Handle(context.Background(), event)

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

	err := handler.Handle(context.Background(), event)

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
	configSvc.EXPECT().GetAutoRouteAllowedDomains(mock.Anything).Return([]string{"app.example.com"}, nil)

	// No existing routes
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{})

	// Expect route to be added
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:latest",
		HTTPS:  true,
	}).Return(nil)

	// Expect deploy to be triggered
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:latest",
		HTTPS:  true,
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

	err := handler.Handle(context.Background(), event)

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
	configSvc.EXPECT().GetAutoRouteAllowedDomains(mock.Anything).Return([]string{"*"}, nil)

	// Existing route with different image
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{
		{Domain: "app.example.com", Image: "myapp:v1", HTTPS: true},
	})

	// Expect route to be updated
	configSvc.EXPECT().UpdateRoute(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:v2",
		HTTPS:  true,
	}).Return(nil)

	// Expect deploy to be triggered
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:v2",
		HTTPS:  true,
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

	err := handler.Handle(context.Background(), event)

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
	configSvc.EXPECT().GetAutoRouteAllowedDomains(mock.Anything).Return([]string{"*"}, nil)

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

	err := handler.Handle(context.Background(), event)

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

	err := handler.Handle(context.Background(), event)

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
	configSvc.EXPECT().GetAutoRouteAllowedDomains(mock.Anything).Return([]string{"app.example.com", "api.example.com", "www.example.com"}, nil).Once()

	// No existing routes
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{}).Times(3)

	// Expect routes to be added for all three domains
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{
		Domain: "app.example.com",
		Image:  "myapp:latest",
		HTTPS:  true,
	}).Return(nil)
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{
		Domain: "api.example.com",
		Image:  "myapp:latest",
		HTTPS:  true,
	}).Return(nil)
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{
		Domain: "www.example.com",
		Image:  "myapp:latest",
		HTTPS:  true,
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

	err := handler.Handle(context.Background(), event)

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

func TestAutoRouteHandler_NewDomain_Allowed(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{})
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{Domain: "app.example.com", Image: "myapp:latest", HTTPS: true}).Return(nil)
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{Domain: "app.example.com", Image: "myapp:latest", HTTPS: true}).Return(&domain.Container{ID: "c1"}, nil)

	created := handler.createOrUpdateRoute(context.Background(), "app.example.com", "myapp:latest", []string{"*.example.com"})
	assert.True(t, created)
}

func TestAutoRouteHandler_NewDomain_NotAllowed(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{})

	created := handler.createOrUpdateRoute(context.Background(), "app.example.com", "myapp:latest", []string{"*.allowed.com"})
	assert.False(t, created)
}

func TestAutoRouteHandler_ExistingDomain_SameRepo(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", Image: "myapp:v1", HTTPS: true}})
	configSvc.EXPECT().UpdateRoute(mock.Anything, domain.Route{Domain: "app.example.com", Image: "myapp:v2", HTTPS: true}).Return(nil)
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{Domain: "app.example.com", Image: "myapp:v2", HTTPS: true}).Return(&domain.Container{ID: "c1"}, nil)

	created := handler.createOrUpdateRoute(context.Background(), "app.example.com", "myapp:v2", []string{"*.example.com"})
	assert.False(t, created)
}

func TestAutoRouteHandler_ExistingDomain_DifferentRepo(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com")

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", Image: "oldapp:v1"}})

	created := handler.createOrUpdateRoute(context.Background(), "app.example.com", "newapp:v2", []string{"*.example.com"})
	assert.False(t, created)
}

func TestAutoRouteHandler_EnvExtractionOnlyOnCreate(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	extractor := &testEnvExtractor{}

	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com").WithEnvExtractor(extractor, t.TempDir())

	configSvc.EXPECT().GetAutoRouteAllowedDomains(mock.Anything).Return([]string{"*.example.com"}, nil).Twice()
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{}).Once()
	configSvc.EXPECT().AddRoute(mock.Anything, domain.Route{Domain: "app.example.com", Image: "myapp:latest", HTTPS: true}).Return(nil)
	containerSvc.EXPECT().Deploy(mock.Anything, domain.Route{Domain: "app.example.com", Image: "myapp:latest", HTTPS: true}).Return(&domain.Container{ID: "c1"}, nil)
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.example.com", Image: "myapp:latest"}}).Once()

	handler.processRoutes(context.Background(), []string{"app.example.com"}, "myapp:latest", &domain.ImageLabels{EnvFile: ".env"})
	handler.processRoutes(context.Background(), []string{"app.example.com"}, "myapp:latest", &domain.ImageLabels{EnvFile: ".env"})

	assert.Equal(t, 1, extractor.calls)
	assert.Equal(t, "registry.example.com/myapp:latest", extractor.lastImage)
	assert.Equal(t, ".env", extractor.lastPath)
}

func TestAutoRouteHandler_ExtractAndMergeEnvFile_ReturnsExistingParseError(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	extractor := &testEnvExtractor{data: []byte("FOO=bar\n")}

	envDir := t.TempDir()
	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com").WithEnvExtractor(extractor, envDir)

	envFileName, err := domainToEnvFileName("app.example.com")
	require.NoError(t, err)
	envFileDst := filepath.Join(envDir, envFileName)
	err = os.WriteFile(envFileDst, []byte("BIG="+strings.Repeat("a", (1<<20)+1)), 0600)
	require.NoError(t, err)

	err = handler.extractAndMergeEnvFile(context.Background(), "myapp:latest", "app.example.com", ".env")
	require.Error(t, err)
	assert.Contains(t, err.Error(), envFileDst)
	assert.Contains(t, err.Error(), "failed to parse existing env file")
}

type testEnvExtractor struct {
	calls     int
	lastImage string
	lastPath  string
	data      []byte
	err       error
}

func (e *testEnvExtractor) ExtractEnvFileFromImage(_ context.Context, imageRef, envFilePath string) ([]byte, error) {
	e.calls++
	e.lastImage = imageRef
	e.lastPath = envFilePath
	if e.err != nil {
		return nil, e.err
	}
	if e.data != nil {
		return e.data, nil
	}
	return []byte("FOO=bar\n"), nil
}

// collectDomains tests

func TestAutoRouteHandler_ExtractAndMergeEnvFile_RejectsSecretReferences(t *testing.T) {
	tests := []struct {
		name        string
		envData     string
		wantErr     bool
		errContains string
	}{
		{
			name:        "pass reference in value",
			envData:     "DB_PASSWORD=${pass:myapp/db}\n",
			wantErr:     true,
			errContains: "secret reference",
		},
		{
			name:        "sops reference in value",
			envData:     "API_KEY=${sops:secrets.yaml#/key}\n",
			wantErr:     true,
			errContains: "secret reference",
		},
		{
			name:        "pass reference embedded in text",
			envData:     "CONNECTION=prefix-${pass:secret}-suffix\n",
			wantErr:     true,
			errContains: "secret reference",
		},
		{
			name:        "multiple secret references",
			envData:     "A=${pass:a}\nB=${sops:b}\n",
			wantErr:     true,
			errContains: "secret reference",
		},
		{
			name:    "normal env values are allowed",
			envData: "FOO=bar\nBAZ=qux\n",
			wantErr: false,
		},
		{
			name:    "dollar sign without reference is allowed",
			envData: "PRICE=$100\n",
			wantErr: false,
		},
		{
			name:    "empty braces are allowed",
			envData: "EMPTY=${}\n",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
			configSvc := inmocks.NewMockConfigService(t)
			containerSvc := inmocks.NewMockContainerService(t)
			blobStorage := mocks.NewMockBlobStorage(t)
			extractor := &testEnvExtractor{data: []byte(tt.envData)}

			envDir := t.TempDir()
			handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com").
				WithEnvExtractor(extractor, envDir)

			err := handler.extractAndMergeEnvFile(context.Background(), "myapp:latest", "app.example.com", ".env")

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}

			assert.NoError(t, err)
		})
	}
}

func TestAutoRouteHandler_ExtractAndMergeEnvFile_DoesNotPersistSecretReferences(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	extractor := &testEnvExtractor{data: []byte("DB_PASSWORD=${pass:myapp/db}\n")}

	envDir := t.TempDir()
	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com").WithEnvExtractor(extractor, envDir)

	envFileName, err := domainToEnvFileName("app.example.com")
	require.NoError(t, err)
	envFileDst := filepath.Join(envDir, envFileName)

	err = handler.extractAndMergeEnvFile(context.Background(), "myapp:latest", "app.example.com", ".env")
	require.ErrorIs(t, err, domain.ErrEnvContainsSecretRef)
	assert.NoFileExists(t, envFileDst)
}

func TestAutoRouteHandler_ExtractAndMergeEnvFile_ReturnsReadErrorForExistingFileIssues(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)
	extractor := &testEnvExtractor{data: []byte("FOO=bar\n")}

	envDir := t.TempDir()
	handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, "registry.example.com").WithEnvExtractor(extractor, envDir)

	envFileName, err := domainToEnvFileName("app.example.com")
	require.NoError(t, err)
	envFileDst := filepath.Join(envDir, envFileName)
	require.NoError(t, os.Mkdir(envFileDst, 0700))

	err = handler.extractAndMergeEnvFile(context.Background(), "myapp:latest", "app.example.com", ".env")
	require.Error(t, err)
	assert.Contains(t, err.Error(), envFileDst)
	assert.Contains(t, err.Error(), "failed to read existing env file")
}

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
			expected:  "myapp@sha256:abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := handler.buildImageName(tt.imageName, tt.reference)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// buildFullImageRef tests

func TestAutoRouteHandler_BuildFullImageRef(t *testing.T) {
	ctx := zerowrap.WithCtx(context.Background(), zerowrap.Default())
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	blobStorage := mocks.NewMockBlobStorage(t)

	tests := []struct {
		name           string
		registryDomain string
		imageName      string
		expected       string
	}{
		{
			name:           "simple image with registry",
			registryDomain: "registry.example.com",
			imageName:      "myapp:latest",
			expected:       "registry.example.com/myapp:latest",
		},
		{
			name:           "already has registry prefix",
			registryDomain: "registry.example.com",
			imageName:      "registry.example.com/myapp:latest",
			expected:       "registry.example.com/myapp:latest",
		},
		{
			name:           "external registry reference",
			registryDomain: "registry.example.com",
			imageName:      "docker.io/library/nginx:latest",
			expected:       "docker.io/library/nginx:latest",
		},
		{
			name:           "digest reference",
			registryDomain: "registry.example.com",
			imageName:      "myapp@sha256:abc123def456",
			expected:       "registry.example.com/myapp@sha256:abc123def456",
		},
		{
			name:           "empty registry domain",
			registryDomain: "",
			imageName:      "myapp:latest",
			expected:       "myapp:latest",
		},
		{
			name:           "image without tag",
			registryDomain: "registry.example.com",
			imageName:      "myapp",
			expected:       "registry.example.com/myapp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewAutoRouteHandler(ctx, configSvc, containerSvc, blobStorage, tt.registryDomain)
			result := handler.buildFullImageRef(tt.imageName)
			assert.Equal(t, tt.expected, result)
		})
	}
}
