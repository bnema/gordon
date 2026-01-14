package manifest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseManifestAnnotations(t *testing.T) {
	tests := []struct {
		name                string
		manifestData        []byte
		contentType         string
		expectedAnnotations map[string]string
		expectedError       bool
		errorContains       string
	}{
		{
			name: "OCI manifest with annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": {
					"mediaType": "application/vnd.oci.image.config.v1+json",
					"digest": "sha256:abc123",
					"size": 1234
				},
				"layers": [],
				"annotations": {
					"version": "1.0.0",
					"created-by": "gordon",
					"description": "Test image"
				}
			}`),
			contentType: "application/vnd.oci.image.manifest.v1+json",
			expectedAnnotations: map[string]string{
				"version":     "1.0.0",
				"created-by":  "gordon",
				"description": "Test image",
			},
		},
		{
			name: "OCI manifest without annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": {
					"mediaType": "application/vnd.oci.image.config.v1+json",
					"digest": "sha256:def456",
					"size": 5678
				},
				"layers": []
			}`),
			contentType:         "application/vnd.oci.image.manifest.v1+json",
			expectedAnnotations: map[string]string{},
		},
		{
			name: "OCI manifest with empty annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": {
					"mediaType": "application/vnd.oci.image.config.v1+json",
					"digest": "sha256:empty123",
					"size": 100
				},
				"layers": [],
				"annotations": {}
			}`),
			contentType:         "application/vnd.oci.image.manifest.v1+json",
			expectedAnnotations: map[string]string{},
		},
		{
			name: "Docker v2.2 manifest (no annotations support)",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
				"config": {
					"mediaType": "application/vnd.docker.container.image.v1+json",
					"digest": "sha256:docker123",
					"size": 2468
				},
				"layers": []
			}`),
			contentType:         "application/vnd.docker.distribution.manifest.v2+json",
			expectedAnnotations: map[string]string{},
		},
		{
			name: "OCI image index with annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.index.v1+json",
				"manifests": [],
				"annotations": {
					"version": "2.0.0",
					"multi-arch": "true"
				}
			}`),
			contentType: "application/vnd.oci.image.index.v1+json",
			expectedAnnotations: map[string]string{
				"version":    "2.0.0",
				"multi-arch": "true",
			},
		},
		{
			name: "Docker manifest list with annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.docker.distribution.manifest.list.v2+json",
				"manifests": [],
				"annotations": {
					"build-date": "2023-01-01"
				}
			}`),
			contentType: "application/vnd.docker.distribution.manifest.list.v2+json",
			expectedAnnotations: map[string]string{
				"build-date": "2023-01-01",
			},
		},
		{
			name: "manifest list without annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.index.v1+json",
				"manifests": []
			}`),
			contentType:         "application/vnd.oci.image.index.v1+json",
			expectedAnnotations: map[string]string{},
		},
		{
			name: "unsupported media type",
			manifestData: []byte(`{
				"schemaVersion": 1,
				"mediaType": "application/vnd.docker.distribution.manifest.v1+json"
			}`),
			contentType:   "application/vnd.docker.distribution.manifest.v1+json",
			expectedError: true,
			errorContains: "unsupported manifest media type",
		},
		{
			name: "invalid JSON for OCI manifest",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": {
					"invalid": json
				}
			}`),
			contentType:   "application/vnd.oci.image.manifest.v1+json",
			expectedError: true,
			errorContains: "failed to parse OCI manifest",
		},
		{
			name: "invalid JSON for manifest list",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.index.v1+json",
				"manifests": [
					invalid json
				]
			}`),
			contentType:   "application/vnd.oci.image.index.v1+json",
			expectedError: true,
			errorContains: "failed to parse manifest list",
		},
		{
			name: "OCI manifest with special characters in annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": {
					"mediaType": "application/vnd.oci.image.config.v1+json",
					"digest": "sha256:special123",
					"size": 999
				},
				"layers": [],
				"annotations": {
					"unicode": "测试🚀",
					"special-chars": "!@#$%^&*()_+-={}[]|\\:;\"'<>?,./"
				}
			}`),
			contentType: "application/vnd.oci.image.manifest.v1+json",
			expectedAnnotations: map[string]string{
				"unicode":       "测试🚀",
				"special-chars": "!@#$%^&*()_+-={}[]|\\:;\"'<>?,./",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations, err := ParseManifestAnnotations(tt.manifestData, tt.contentType)

			if tt.expectedError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedAnnotations, annotations)
			}
		})
	}
}

func TestParseOCIManifestAnnotations(t *testing.T) {
	tests := []struct {
		name                string
		manifestData        []byte
		expectedAnnotations map[string]string
		expectedError       bool
		errorContains       string
	}{
		{
			name: "valid OCI manifest with annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": {
					"mediaType": "application/vnd.oci.image.config.v1+json",
					"digest": "sha256:test123",
					"size": 1000
				},
				"layers": [],
				"annotations": {
					"key1": "value1",
					"key2": "value2"
				}
			}`),
			expectedAnnotations: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name: "OCI manifest with nil annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"config": {
					"mediaType": "application/vnd.oci.image.config.v1+json",
					"digest": "sha256:nil123",
					"size": 500
				},
				"layers": []
			}`),
			expectedAnnotations: map[string]string{},
		},
		{
			name: "invalid JSON",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"config": invalid json
			}`),
			expectedError: true,
			errorContains: "failed to parse OCI manifest",
		},
		{
			name:                "empty manifest data",
			manifestData:        []byte(`{}`),
			expectedAnnotations: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations, err := parseOCIManifestAnnotations(tt.manifestData)

			if tt.expectedError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedAnnotations, annotations)
			}
		})
	}
}

func TestParseManifestListAnnotations(t *testing.T) {
	tests := []struct {
		name                string
		manifestData        []byte
		expectedAnnotations map[string]string
		expectedError       bool
		errorContains       string
	}{
		{
			name: "valid manifest list with annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.index.v1+json",
				"manifests": [],
				"annotations": {
					"platform": "multi",
					"supported": "yes"
				}
			}`),
			expectedAnnotations: map[string]string{
				"platform":  "multi",
				"supported": "yes",
			},
		},
		{
			name: "manifest list with nil annotations",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"mediaType": "application/vnd.oci.image.index.v1+json",
				"manifests": []
			}`),
			expectedAnnotations: map[string]string{},
		},
		{
			name: "invalid JSON",
			manifestData: []byte(`{
				"schemaVersion": 2,
				"manifests": invalid json
			}`),
			expectedError: true,
			errorContains: "failed to parse manifest list",
		},
		{
			name:                "empty manifest list",
			manifestData:        []byte(`{}`),
			expectedAnnotations: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations, err := parseManifestListAnnotations(tt.manifestData)

			if tt.expectedError {
				require.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedAnnotations, annotations)
			}
		})
	}
}

func TestIsVersionedDeployment(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		expected    bool
	}{
		{
			name: "has version annotation",
			annotations: map[string]string{
				"version":    "1.0.0",
				"created-by": "gordon",
			},
			expected: true,
		},
		{
			name: "has empty version annotation",
			annotations: map[string]string{
				"version": "",
				"other":   "value",
			},
			expected: true,
		},
		{
			name: "no version annotation",
			annotations: map[string]string{
				"created-by":  "gordon",
				"description": "test image",
			},
			expected: false,
		},
		{
			name:        "empty annotations map",
			annotations: map[string]string{},
			expected:    false,
		},
		{
			name:        "nil annotations map",
			annotations: nil,
			expected:    false,
		},
		{
			name: "version annotation with whitespace",
			annotations: map[string]string{
				"version": "  1.2.3  ",
			},
			expected: true,
		},
		{
			name: "version annotation case sensitivity",
			annotations: map[string]string{
				"Version": "1.0.0", // Different case
				"VERSION": "2.0.0", // All caps
			},
			expected: false, // Should be case-sensitive
		},
		{
			name: "multiple version-like annotations",
			annotations: map[string]string{
				"app-version":   "1.0.0",
				"image-version": "2.0.0",
				"semver":        "3.0.0",
			},
			expected: false, // Only exact "version" key should match
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsVersionedDeployment(tt.annotations)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetDeploymentVersion(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		expected    string
	}{
		{
			name: "has version annotation",
			annotations: map[string]string{
				"version":    "1.0.0",
				"created-by": "gordon",
			},
			expected: "1.0.0",
		},
		{
			name: "version with semantic versioning",
			annotations: map[string]string{
				"version": "2.1.3-alpha.1+build.123",
			},
			expected: "2.1.3-alpha.1+build.123",
		},
		{
			name: "version with commit hash",
			annotations: map[string]string{
				"version": "abc123def456",
			},
			expected: "abc123def456",
		},
		{
			name: "version with special characters",
			annotations: map[string]string{
				"version": "v1.0.0-rc.1_build-2023-01-01",
			},
			expected: "v1.0.0-rc.1_build-2023-01-01",
		},
		{
			name: "empty version annotation",
			annotations: map[string]string{
				"version": "",
				"other":   "value",
			},
			expected: "",
		},
		{
			name: "version with only whitespace",
			annotations: map[string]string{
				"version": "   ",
			},
			expected: "   ", // Should return as-is, not trimmed
		},
		{
			name: "no version annotation",
			annotations: map[string]string{
				"created-by":  "gordon",
				"description": "test image",
			},
			expected: "",
		},
		{
			name:        "empty annotations map",
			annotations: map[string]string{},
			expected:    "",
		},
		{
			name:        "nil annotations map",
			annotations: nil,
			expected:    "",
		},
		{
			name: "version annotation case sensitivity",
			annotations: map[string]string{
				"Version": "1.0.0", // Different case
				"VERSION": "2.0.0", // All caps
				"other":   "value",
			},
			expected: "", // Should be case-sensitive, only "version" matches
		},
		{
			name: "multiple version-like annotations",
			annotations: map[string]string{
				"app-version":   "1.0.0",
				"image-version": "2.0.0",
				"version":       "3.0.0", // Only this should be returned
				"semver":        "4.0.0",
			},
			expected: "3.0.0",
		},
		{
			name: "version with unicode characters",
			annotations: map[string]string{
				"version": "测试版本-1.0.0",
			},
			expected: "测试版本-1.0.0",
		},
		{
			name: "long version string",
			annotations: map[string]string{
				"version": "1.0.0-alpha.beta.gamma.delta.epsilon.zeta.eta.theta.iota.kappa.lambda.mu.nu.xi.omicron.pi.rho.sigma.tau.upsilon.phi.chi.psi.omega",
			},
			expected: "1.0.0-alpha.beta.gamma.delta.epsilon.zeta.eta.theta.iota.kappa.lambda.mu.nu.xi.omicron.pi.rho.sigma.tau.upsilon.phi.chi.psi.omega",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetDeploymentVersion(tt.annotations)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAnnotations_Integration(t *testing.T) {
	t.Run("parse and check versioned deployment", func(t *testing.T) {
		manifestData := []byte(`{
			"schemaVersion": 2,
			"mediaType": "application/vnd.oci.image.manifest.v1+json",
			"config": {
				"mediaType": "application/vnd.oci.image.config.v1+json",
				"digest": "sha256:integration123",
				"size": 1000
			},
			"layers": [],
			"annotations": {
				"version": "1.2.3",
				"created-by": "gordon-ci",
				"build-date": "2023-01-01T00:00:00Z"
			}
		}`)

		// Parse annotations
		annotations, err := ParseManifestAnnotations(manifestData, "application/vnd.oci.image.manifest.v1+json")
		require.NoError(t, err)

		// Check if it's a versioned deployment
		isVersioned := IsVersionedDeployment(annotations)
		assert.True(t, isVersioned)

		// Get the version
		version := GetDeploymentVersion(annotations)
		assert.Equal(t, "1.2.3", version)
	})

	t.Run("parse Docker manifest and check versioning", func(t *testing.T) {
		manifestData := []byte(`{
			"schemaVersion": 2,
			"mediaType": "application/vnd.docker.distribution.manifest.v2+json",
			"config": {
				"mediaType": "application/vnd.docker.container.image.v1+json",
				"digest": "sha256:docker123",
				"size": 500
			},
			"layers": []
		}`)

		// Parse annotations (should be empty for Docker manifest)
		annotations, err := ParseManifestAnnotations(manifestData, "application/vnd.docker.distribution.manifest.v2+json")
		require.NoError(t, err)
		assert.Empty(t, annotations)

		// Check versioning (should be false)
		isVersioned := IsVersionedDeployment(annotations)
		assert.False(t, isVersioned)

		version := GetDeploymentVersion(annotations)
		assert.Empty(t, version)
	})

	t.Run("parse manifest list with version", func(t *testing.T) {
		manifestData := []byte(`{
			"schemaVersion": 2,
			"mediaType": "application/vnd.oci.image.index.v1+json",
			"manifests": [
				{
					"mediaType": "application/vnd.oci.image.manifest.v1+json",
					"digest": "sha256:amd64",
					"size": 1000,
					"platform": {
						"architecture": "amd64",
						"os": "linux"
					}
				}
			],
			"annotations": {
				"version": "2.0.0-beta.1",
				"multi-arch": "true"
			}
		}`)

		// Parse annotations
		annotations, err := ParseManifestAnnotations(manifestData, "application/vnd.oci.image.index.v1+json")
		require.NoError(t, err)

		// Verify specific annotations
		assert.Equal(t, "2.0.0-beta.1", annotations["version"])
		assert.Equal(t, "true", annotations["multi-arch"])

		// Check versioning functions
		isVersioned := IsVersionedDeployment(annotations)
		assert.True(t, isVersioned)

		version := GetDeploymentVersion(annotations)
		assert.Equal(t, "2.0.0-beta.1", version)
	})

	t.Run("round trip JSON parsing", func(t *testing.T) {
		// Create an OCI manifest struct
		originalManifest := OCIManifest{
			SchemaVersion: 2,
			MediaType:     "application/vnd.oci.image.manifest.v1+json",
			Config: OCIDescriptor{
				MediaType: "application/vnd.oci.image.config.v1+json",
				Digest:    "sha256:config123",
				Size:      999,
			},
			Layers: []OCIDescriptor{},
			Annotations: map[string]string{
				"version":     "1.0.0",
				"description": "Round trip test",
			},
		}

		// Marshal to JSON
		jsonData, err := json.Marshal(originalManifest)
		require.NoError(t, err)

		// Parse annotations using our function
		annotations, err := ParseManifestAnnotations(jsonData, "application/vnd.oci.image.manifest.v1+json")
		require.NoError(t, err)

		// Verify annotations match
		assert.Equal(t, originalManifest.Annotations, annotations)

		// Verify versioning functions work
		assert.True(t, IsVersionedDeployment(annotations))
		assert.Equal(t, "1.0.0", GetDeploymentVersion(annotations))
	})
}
