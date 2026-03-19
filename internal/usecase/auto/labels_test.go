package auto

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

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

			result, err := ParseConfigDigest(data)

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
	_, err := ParseConfigDigest([]byte("invalid json"))
	assert.Error(t, err)
}

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
		{
			name: "deprecated port label fallback",
			config: map[string]any{
				"config": map[string]any{
					"Labels": map[string]any{
						"gordon.port": "9090",
					},
				},
			},
			expected: &domain.ImageLabels{
				Port: "9090",
			},
		},
		{
			name: "proxy.port takes precedence over port",
			config: map[string]any{
				"config": map[string]any{
					"Labels": map[string]any{
						"gordon.proxy.port": "8080",
						"gordon.port":       "9090",
					},
				},
			},
			expected: &domain.ImageLabels{
				Port: "8080",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.config)
			require.NoError(t, err)

			result, err := ParseImageLabels(data)
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
	_, err := ParseImageLabels([]byte("invalid json"))
	assert.Error(t, err)
}

func TestExtractLabels(t *testing.T) {
	ctx := context.Background()

	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"digest": "sha256:configdigest123",
		},
	}
	manifestData, err := json.Marshal(manifest)
	require.NoError(t, err)

	imageConf := map[string]any{
		"config": map[string]any{
			"Labels": map[string]any{
				"gordon.domain": "app.example.com",
				"gordon.health": "/healthz",
			},
		},
	}
	configData, err := json.Marshal(imageConf)
	require.NoError(t, err)

	blobStorage := mocks.NewMockBlobStorage(t)
	blobStorage.EXPECT().GetBlob("sha256:configdigest123").Return(io.NopCloser(strings.NewReader(string(configData))), nil)

	labels, err := ExtractLabels(ctx, manifestData, blobStorage)
	require.NoError(t, err)
	assert.Equal(t, "app.example.com", labels.Domain)
	assert.Equal(t, "/healthz", labels.Health)
}

func TestExtractLabels_InvalidManifest(t *testing.T) {
	ctx := context.Background()
	blobStorage := mocks.NewMockBlobStorage(t)

	_, err := ExtractLabels(ctx, []byte("bad json"), blobStorage)
	assert.Error(t, err)
}

func TestExtractLabels_EmptyConfigDigest(t *testing.T) {
	ctx := context.Background()

	manifest := map[string]any{
		"schemaVersion": 2,
		"config": map[string]any{
			"digest": "",
		},
	}
	manifestData, err := json.Marshal(manifest)
	require.NoError(t, err)

	blobStorage := mocks.NewMockBlobStorage(t)

	_, err = ExtractLabels(ctx, manifestData, blobStorage)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no config digest")
}
