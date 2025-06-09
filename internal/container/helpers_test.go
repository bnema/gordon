package container

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeImageRef(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple image with tag",
			input:    "nginx:latest",
			expected: "docker.io/library/nginx:latest",
		},
		{
			name:     "image without tag",
			input:    "nginx",
			expected: "docker.io/library/nginx:latest",
		},
		{
			name:     "registry with image and tag",
			input:    "registry.example.com/nginx:v1.0",
			expected: "registry.example.com/nginx:v1.0",
		},
		{
			name:     "dockerhub image with user",
			input:    "user/repo:tag",
			expected: "docker.io/user/repo:tag",
		},
		{
			name:     "official library image without tag",
			input:    "postgres",
			expected: "docker.io/library/postgres:latest",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := normalizeImageRef(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestGenerateVolumeName(t *testing.T) {
	testCases := []struct {
		name     string
		prefix   string
		domain   string
		path     string
	}{
		{
			name:   "basic volume name",
			prefix: "gordon",
			domain: "app.example.com",
			path:   "/data",
		},
		{
			name:   "complex path",
			prefix: "test",
			domain: "my-app.domain.com", 
			path:   "/var/www/html/uploads",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := generateVolumeName(tc.prefix, tc.domain, tc.path)
			
			// Check that it starts with prefix
			assert.True(t, strings.HasPrefix(result, tc.prefix+"-"))
			
			// Check that it contains sanitized domain
			sanitizedDomain := strings.ReplaceAll(strings.ToLower(tc.domain), ".", "-")
			assert.Contains(t, result, sanitizedDomain)
			
			// Volume names should be consistent for same inputs
			result2 := generateVolumeName(tc.prefix, tc.domain, tc.path)
			assert.Equal(t, result, result2)
			
			// Volume names should be different for different paths
			result3 := generateVolumeName(tc.prefix, tc.domain, tc.path+"/different")
			assert.NotEqual(t, result, result3)
		})
	}
}

func TestMergeEnvironmentVariables(t *testing.T) {
	testCases := []struct {
		name         string
		dockerfileEnv []string
		userEnv      []string
		expected     []string
	}{
		{
			name:         "user env overrides dockerfile env",
			dockerfileEnv: []string{"NODE_ENV=development", "PORT=3000"},
			userEnv:      []string{"NODE_ENV=production", "API_KEY=secret"},
			expected:     []string{"NODE_ENV=production", "PORT=3000", "API_KEY=secret"},
		},
		{
			name:         "only dockerfile env",
			dockerfileEnv: []string{"NODE_ENV=development", "PORT=3000"},
			userEnv:      []string{},
			expected:     []string{"NODE_ENV=development", "PORT=3000"},
		},
		{
			name:         "only user env",
			dockerfileEnv: []string{},
			userEnv:      []string{"API_KEY=secret", "DEBUG=true"},
			expected:     []string{"API_KEY=secret", "DEBUG=true"},
		},
		{
			name:         "empty environment",
			dockerfileEnv: []string{},
			userEnv:      []string{},
			expected:     []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := mergeEnvironmentVariables(tc.dockerfileEnv, tc.userEnv)
			assert.Equal(t, len(tc.expected), len(result))
			
			// Convert to map for easier comparison
			resultMap := make(map[string]string)
			for _, env := range result {
				parts := splitEnvVar(env)
				if len(parts) == 2 {
					resultMap[parts[0]] = parts[1]
				}
			}
			
			expectedMap := make(map[string]string)
			for _, env := range tc.expected {
				parts := splitEnvVar(env)
				if len(parts) == 2 {
					expectedMap[parts[0]] = parts[1]
				}
			}
			
			assert.Equal(t, expectedMap, resultMap)
		})
	}
}

// Helper function to split environment variable string
func splitEnvVar(env string) []string {
	for i, c := range env {
		if c == '=' {
			return []string{env[:i], env[i+1:]}
		}
	}
	return []string{env}
}

func TestSanitizeDomainForVolume(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "basic domain",
			input:    "app.example.com",
			expected: "app-example-com",
		},
		{
			name:     "domain with port",
			input:    "localhost:3000",
			expected: "localhost-3000",
		},
		{
			name:     "domain with special chars",
			input:    "my_app@example.com",
			expected: "my-app-example-com",
		},
		{
			name:     "uppercase domain",
			input:    "API.EXAMPLE.COM",
			expected: "api-example-com",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := sanitizeDomainForVolume(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}