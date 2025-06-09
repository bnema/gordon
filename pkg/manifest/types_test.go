package manifest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOCIManifest_JSON(t *testing.T) {
	tests := []struct {
		name     string
		manifest OCIManifest
		wantJSON string
	}{
		{
			name: "basic OCI manifest",
			manifest: OCIManifest{
				SchemaVersion: 2,
				MediaType:     "application/vnd.oci.image.manifest.v1+json",
				Config: OCIDescriptor{
					MediaType: "application/vnd.oci.image.config.v1+json",
					Digest:    "sha256:abc123",
					Size:      1234,
				},
				Layers: []OCIDescriptor{
					{
						MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
						Digest:    "sha256:layer1",
						Size:      5678,
					},
				},
			},
			wantJSON: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:abc123","size":1234},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:layer1","size":5678}]}`,
		},
		{
			name: "OCI manifest with annotations",
			manifest: OCIManifest{
				SchemaVersion: 2,
				MediaType:     "application/vnd.oci.image.manifest.v1+json",
				Config: OCIDescriptor{
					MediaType: "application/vnd.oci.image.config.v1+json",
					Digest:    "sha256:config123",
					Size:      1024,
				},
				Layers: []OCIDescriptor{
					{
						MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
						Digest:    "sha256:layer1",
						Size:      2048,
					},
				},
				Annotations: map[string]string{
					"version":    "1.0.0",
					"created-by": "gordon",
				},
			},
			wantJSON: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:config123","size":1024},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:layer1","size":2048}],"annotations":{"created-by":"gordon","version":"1.0.0"}}`,
		},
		{
			name: "OCI manifest with multiple layers",
			manifest: OCIManifest{
				SchemaVersion: 2,
				MediaType:     "application/vnd.oci.image.manifest.v1+json",
				Config: OCIDescriptor{
					MediaType: "application/vnd.oci.image.config.v1+json",
					Digest:    "sha256:config456",
					Size:      512,
				},
				Layers: []OCIDescriptor{
					{
						MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
						Digest:    "sha256:layer1",
						Size:      1024,
					},
					{
						MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
						Digest:    "sha256:layer2",
						Size:      2048,
					},
					{
						MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
						Digest:    "sha256:layer3",
						Size:      4096,
					},
				},
			},
			wantJSON: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:config456","size":512},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:layer1","size":1024},{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:layer2","size":2048},{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:layer3","size":4096}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test marshaling
			gotJSON, err := json.Marshal(tt.manifest)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(gotJSON))

			// Test unmarshaling
			var gotManifest OCIManifest
			err = json.Unmarshal([]byte(tt.wantJSON), &gotManifest)
			require.NoError(t, err)
			assert.Equal(t, tt.manifest, gotManifest)
		})
	}
}

func TestDockerManifest_JSON(t *testing.T) {
	tests := []struct {
		name     string
		manifest DockerManifest
		wantJSON string
	}{
		{
			name: "basic Docker manifest",
			manifest: DockerManifest{
				SchemaVersion: 2,
				MediaType:     "application/vnd.docker.distribution.manifest.v2+json",
				Config: OCIDescriptor{
					MediaType: "application/vnd.docker.container.image.v1+json",
					Digest:    "sha256:docker123",
					Size:      1500,
				},
				Layers: []OCIDescriptor{
					{
						MediaType: "application/vnd.docker.image.rootfs.diff.tar.gzip",
						Digest:    "sha256:dockerlayer1",
						Size:      7890,
					},
				},
			},
			wantJSON: `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:docker123","size":1500},"layers":[{"mediaType":"application/vnd.docker.image.rootfs.diff.tar.gzip","digest":"sha256:dockerlayer1","size":7890}]}`,
		},
		{
			name: "Docker manifest with empty layers",
			manifest: DockerManifest{
				SchemaVersion: 2,
				MediaType:     "application/vnd.docker.distribution.manifest.v2+json",
				Config: OCIDescriptor{
					MediaType: "application/vnd.docker.container.image.v1+json",
					Digest:    "sha256:empty123",
					Size:      256,
				},
				Layers: []OCIDescriptor{},
			},
			wantJSON: `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","digest":"sha256:empty123","size":256},"layers":[]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test marshaling
			gotJSON, err := json.Marshal(tt.manifest)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(gotJSON))

			// Test unmarshaling
			var gotManifest DockerManifest
			err = json.Unmarshal([]byte(tt.wantJSON), &gotManifest)
			require.NoError(t, err)
			assert.Equal(t, tt.manifest, gotManifest)
		})
	}
}

func TestOCIDescriptor_JSON(t *testing.T) {
	tests := []struct {
		name       string
		descriptor OCIDescriptor
		wantJSON   string
	}{
		{
			name: "basic descriptor",
			descriptor: OCIDescriptor{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
				Digest:    "sha256:descriptor123",
				Size:      9999,
			},
			wantJSON: `{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:descriptor123","size":9999}`,
		},
		{
			name: "descriptor with annotations",
			descriptor: OCIDescriptor{
				MediaType: "application/vnd.oci.image.config.v1+json",
				Digest:    "sha256:annotated456",
				Size:      1111,
				Annotations: map[string]string{
					"title":       "Test Layer",
					"description": "A test layer for unit testing",
				},
			},
			wantJSON: `{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:annotated456","size":1111,"annotations":{"description":"A test layer for unit testing","title":"Test Layer"}}`,
		},
		{
			name: "descriptor with zero size",
			descriptor: OCIDescriptor{
				MediaType: "application/vnd.oci.empty.v1+json",
				Digest:    "sha256:empty",
				Size:      0,
			},
			wantJSON: `{"mediaType":"application/vnd.oci.empty.v1+json","digest":"sha256:empty","size":0}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test marshaling
			gotJSON, err := json.Marshal(tt.descriptor)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(gotJSON))

			// Test unmarshaling
			var gotDescriptor OCIDescriptor
			err = json.Unmarshal([]byte(tt.wantJSON), &gotDescriptor)
			require.NoError(t, err)
			assert.Equal(t, tt.descriptor, gotDescriptor)
		})
	}
}

func TestManifestList_JSON(t *testing.T) {
	tests := []struct {
		name         string
		manifestList ManifestList
		wantJSON     string
	}{
		{
			name: "basic manifest list",
			manifestList: ManifestList{
				SchemaVersion: 2,
				MediaType:     "application/vnd.oci.image.index.v1+json",
				Manifests: []ManifestDescriptor{
					{
						MediaType: "application/vnd.oci.image.manifest.v1+json",
						Digest:    "sha256:manifest1",
						Size:      2222,
						Platform: &Platform{
							Architecture: "amd64",
							OS:           "linux",
						},
					},
				},
			},
			wantJSON: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:manifest1","size":2222,"platform":{"architecture":"amd64","os":"linux"}}]}`,
		},
		{
			name: "manifest list with annotations",
			manifestList: ManifestList{
				SchemaVersion: 2,
				MediaType:     "application/vnd.oci.image.index.v1+json",
				Manifests: []ManifestDescriptor{
					{
						MediaType: "application/vnd.oci.image.manifest.v1+json",
						Digest:    "sha256:manifest2",
						Size:      3333,
						Platform: &Platform{
							Architecture: "arm64",
							OS:           "linux",
							Variant:      "v8",
						},
						Annotations: map[string]string{
							"io.github.gordon.platform": "arm64-v8",
						},
					},
				},
				Annotations: map[string]string{
					"version": "2.0.0",
					"source":  "github.com/user/repo",
				},
			},
			wantJSON: `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:manifest2","size":3333,"platform":{"architecture":"arm64","os":"linux","variant":"v8"},"annotations":{"io.github.gordon.platform":"arm64-v8"}}],"annotations":{"source":"github.com/user/repo","version":"2.0.0"}}`,
		},
		{
			name: "multi-platform manifest list",
			manifestList: ManifestList{
				SchemaVersion: 2,
				MediaType:     "application/vnd.docker.distribution.manifest.list.v2+json",
				Manifests: []ManifestDescriptor{
					{
						MediaType: "application/vnd.docker.distribution.manifest.v2+json",
						Digest:    "sha256:linux-amd64",
						Size:      4444,
						Platform: &Platform{
							Architecture: "amd64",
							OS:           "linux",
						},
					},
					{
						MediaType: "application/vnd.docker.distribution.manifest.v2+json",
						Digest:    "sha256:linux-arm64",
						Size:      5555,
						Platform: &Platform{
							Architecture: "arm64",
							OS:           "linux",
						},
					},
					{
						MediaType: "application/vnd.docker.distribution.manifest.v2+json",
						Digest:    "sha256:windows-amd64",
						Size:      6666,
						Platform: &Platform{
							Architecture: "amd64",
							OS:           "windows",
							OSVersion:    "10.0.17763.1234",
						},
					},
				},
			},
			wantJSON: `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","digest":"sha256:linux-amd64","size":4444,"platform":{"architecture":"amd64","os":"linux"}},{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","digest":"sha256:linux-arm64","size":5555,"platform":{"architecture":"arm64","os":"linux"}},{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","digest":"sha256:windows-amd64","size":6666,"platform":{"architecture":"amd64","os":"windows","os.version":"10.0.17763.1234"}}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test marshaling
			gotJSON, err := json.Marshal(tt.manifestList)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(gotJSON))

			// Test unmarshaling
			var gotManifestList ManifestList
			err = json.Unmarshal([]byte(tt.wantJSON), &gotManifestList)
			require.NoError(t, err)
			assert.Equal(t, tt.manifestList, gotManifestList)
		})
	}
}

func TestPlatform_JSON(t *testing.T) {
	tests := []struct {
		name     string
		platform Platform
		wantJSON string
	}{
		{
			name: "basic platform",
			platform: Platform{
				Architecture: "amd64",
				OS:           "linux",
			},
			wantJSON: `{"architecture":"amd64","os":"linux"}`,
		},
		{
			name: "platform with variant",
			platform: Platform{
				Architecture: "arm",
				OS:           "linux",
				Variant:      "v7",
			},
			wantJSON: `{"architecture":"arm","os":"linux","variant":"v7"}`,
		},
		{
			name: "windows platform with version",
			platform: Platform{
				Architecture: "amd64",
				OS:           "windows",
				OSVersion:    "10.0.14393.1066",
			},
			wantJSON: `{"architecture":"amd64","os":"windows","os.version":"10.0.14393.1066"}`,
		},
		{
			name: "platform with features",
			platform: Platform{
				Architecture: "amd64",
				OS:           "linux",
				OSFeatures:   []string{"feature1", "feature2"},
			},
			wantJSON: `{"architecture":"amd64","os":"linux","os.features":["feature1","feature2"]}`,
		},
		{
			name: "complete platform",
			platform: Platform{
				Architecture: "s390x",
				OS:           "linux",
				OSVersion:    "4.15.0",
				OSFeatures:   []string{"capability1", "capability2", "capability3"},
				Variant:      "custom",
			},
			wantJSON: `{"architecture":"s390x","os":"linux","os.version":"4.15.0","os.features":["capability1","capability2","capability3"],"variant":"custom"}`,
		},
		{
			name: "empty platform",
			platform: Platform{
				Architecture: "",
				OS:           "",
			},
			wantJSON: `{"architecture":"","os":""}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test marshaling
			gotJSON, err := json.Marshal(tt.platform)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(gotJSON))

			// Test unmarshaling
			var gotPlatform Platform
			err = json.Unmarshal([]byte(tt.wantJSON), &gotPlatform)
			require.NoError(t, err)
			assert.Equal(t, tt.platform, gotPlatform)
		})
	}
}

func TestManifestDescriptor_JSON(t *testing.T) {
	tests := []struct {
		name       string
		descriptor ManifestDescriptor
		wantJSON   string
	}{
		{
			name: "basic manifest descriptor",
			descriptor: ManifestDescriptor{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    "sha256:manifestdesc123",
				Size:      7777,
			},
			wantJSON: `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:manifestdesc123","size":7777}`,
		},
		{
			name: "manifest descriptor with platform",
			descriptor: ManifestDescriptor{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    "sha256:platform123",
				Size:      8888,
				Platform: &Platform{
					Architecture: "riscv64",
					OS:           "linux",
				},
			},
			wantJSON: `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:platform123","size":8888,"platform":{"architecture":"riscv64","os":"linux"}}`,
		},
		{
			name: "manifest descriptor with annotations",
			descriptor: ManifestDescriptor{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    "sha256:annotations123",
				Size:      9999,
				Annotations: map[string]string{
					"io.github.gordon.build": "automated",
					"version":                "3.1.4",
				},
			},
			wantJSON: `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:annotations123","size":9999,"annotations":{"io.github.gordon.build":"automated","version":"3.1.4"}}`,
		},
		{
			name: "complete manifest descriptor",
			descriptor: ManifestDescriptor{
				MediaType: "application/vnd.oci.image.manifest.v1+json",
				Digest:    "sha256:complete123",
				Size:      12345,
				Platform: &Platform{
					Architecture: "ppc64le",
					OS:           "linux",
					Variant:      "power9",
				},
				Annotations: map[string]string{
					"build.date":   "2023-01-01T00:00:00Z",
					"maintainer":   "gordon-team",
					"io.buildah.version": "1.23.0",
				},
			},
			wantJSON: `{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"sha256:complete123","size":12345,"platform":{"architecture":"ppc64le","os":"linux","variant":"power9"},"annotations":{"build.date":"2023-01-01T00:00:00Z","io.buildah.version":"1.23.0","maintainer":"gordon-team"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test marshaling
			gotJSON, err := json.Marshal(tt.descriptor)
			require.NoError(t, err)
			assert.JSONEq(t, tt.wantJSON, string(gotJSON))

			// Test unmarshaling
			var gotDescriptor ManifestDescriptor
			err = json.Unmarshal([]byte(tt.wantJSON), &gotDescriptor)
			require.NoError(t, err)
			assert.Equal(t, tt.descriptor, gotDescriptor)
		})
	}
}

func TestManifestTypes_EdgeCases(t *testing.T) {
	t.Run("empty annotations should be omitted", func(t *testing.T) {
		manifest := OCIManifest{
			SchemaVersion: 2,
			MediaType:     "application/vnd.oci.image.manifest.v1+json",
			Config: OCIDescriptor{
				MediaType: "application/vnd.oci.image.config.v1+json",
				Digest:    "sha256:config",
				Size:      100,
			},
			Layers: []OCIDescriptor{},
			Annotations: map[string]string{}, // Empty map should be omitted
		}

		jsonData, err := json.Marshal(manifest)
		require.NoError(t, err)
		
		// Should not contain "annotations" field
		assert.NotContains(t, string(jsonData), "annotations")
	})

	t.Run("nil annotations should be omitted", func(t *testing.T) {
		manifest := OCIManifest{
			SchemaVersion: 2,
			MediaType:     "application/vnd.oci.image.manifest.v1+json",
			Config: OCIDescriptor{
				MediaType: "application/vnd.oci.image.config.v1+json",
				Digest:    "sha256:config",
				Size:      100,
			},
			Layers: []OCIDescriptor{},
			// Annotations is nil by default
		}

		jsonData, err := json.Marshal(manifest)
		require.NoError(t, err)
		
		// Should not contain "annotations" field
		assert.NotContains(t, string(jsonData), "annotations")
	})

	t.Run("nil platform should be omitted", func(t *testing.T) {
		descriptor := ManifestDescriptor{
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    "sha256:test",
			Size:      123,
			// Platform is nil by default
		}

		jsonData, err := json.Marshal(descriptor)
		require.NoError(t, err)
		
		// Should not contain "platform" field
		assert.NotContains(t, string(jsonData), "platform")
	})

	t.Run("empty string fields are preserved", func(t *testing.T) {
		platform := Platform{
			Architecture: "",
			OS:           "",
			OSVersion:    "",
			Variant:      "",
		}

		jsonData, err := json.Marshal(platform)
		require.NoError(t, err)
		
		// Empty strings should be preserved
		assert.Contains(t, string(jsonData), `"architecture":""`)
		assert.Contains(t, string(jsonData), `"os":""`)
	})
}