package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateBuildArg(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		wantErr bool
	}{
		{"valid simple", "FOO=bar", false},
		{"valid with underscore key", "_FOO=bar", false},
		{"valid with numbers in key", "FOO123=bar", false},
		{"valid empty value", "FOO=", false},
		{"valid complex value", "FOO=bar baz=qux", false},
		{"invalid no equals", "FOO", true},
		{"invalid starts with number", "1FOO=bar", true},
		{"invalid starts with dash", "-FOO=bar", true},
		{"invalid special chars in key", "FOO-BAR=baz", true},
		{"invalid empty string", "", true},
		{"invalid equals only", "=value", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBuildArg(tt.arg)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestParseImageRef(t *testing.T) {
	tests := []struct {
		name         string
		image        string
		wantRegistry string
		wantName     string
		wantTag      string
	}{
		{"full ref", "reg.example.com/myapp:v1.0.0", "reg.example.com", "myapp", "v1.0.0"},
		{"no tag", "reg.example.com/myapp", "reg.example.com", "myapp", "latest"},
		{"latest tag", "reg.example.com/myapp:latest", "reg.example.com", "myapp", "latest"},
		{"no slash", "myapp:v1.0.0", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, name, tag := parseImageRef(tt.image)
			assert.Equal(t, tt.wantRegistry, registry)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantTag, tag)
		})
	}
}

func TestBuildAndPush_BuildArgs(t *testing.T) {
	// Verify buildImageArgs produces --load instead of --push
	args := buildImageArgs("v1.0.0", "linux/amd64", "Dockerfile", []string{"CGO_ENABLED=0"}, "reg.example.com/app:v1.0.0", "reg.example.com/app:latest")

	assert.Contains(t, args, "--load")
	assert.NotContains(t, args, "--push")
	assert.Contains(t, args, "--platform")
	assert.Contains(t, args, "-f")
	assert.Contains(t, args, "Dockerfile")
}

func TestBuildImageArgs_CustomDockerfile(t *testing.T) {
	args := buildImageArgs("v1.0.0", "linux/amd64", "docker/app/Dockerfile", nil, "reg.example.com/app:v1.0.0", "reg.example.com/app:latest")

	assert.Contains(t, args, "-f")
	assert.Contains(t, args, "docker/app/Dockerfile")
}

func TestParseDockerfileLabels(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected map[string]string
	}{
		{
			name:    "simple label",
			content: `LABEL gordon.domain="myapp.example.com"`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
			},
		},
		{
			name: "multiple labels",
			content: `LABEL gordon.domain="myapp.example.com"
LABEL gordon.port="8080"
LABEL gordon.health="/health"`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
				"gordon.port":   "8080",
				"gordon.health": "/health",
			},
		},
		{
			name:    "multi-label on one line",
			content: `LABEL gordon.domain="myapp.example.com" gordon.port="8080"`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
				"gordon.port":   "8080",
			},
		},
		{
			name:    "unquoted value",
			content: `LABEL gordon.domain=myapp.example.com`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
			},
		},
		{
			name:    "single-quoted value",
			content: `LABEL gordon.domain='myapp.example.com'`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
			},
		},
		{
			name:    "non-gordon labels ignored",
			content: `LABEL maintainer="John" gordon.domain="myapp.example.com"`,
			expected: map[string]string{
				"gordon.domain": "myapp.example.com",
			},
		},
		{
			name:    "comma-separated domains",
			content: `LABEL gordon.domains="app1.example.com,app2.example.com"`,
			expected: map[string]string{
				"gordon.domains": "app1.example.com,app2.example.com",
			},
		},
		{
			name:     "no labels",
			content:  `FROM alpine:latest`,
			expected: map[string]string{},
		},
		{
			name:     "comments and empty lines",
			content:  "# This is a comment\n\nLABEL gordon.domain=\"test.com\"",
			expected: map[string]string{"gordon.domain": "test.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write a temp Dockerfile
			dir := t.TempDir()
			dockerfile := filepath.Join(dir, "Dockerfile")
			err := os.WriteFile(dockerfile, []byte(tt.content), 0644)
			assert.NoError(t, err)

			labels := parseDockerfileLabels(dockerfile)
			assert.Equal(t, tt.expected, labels)
		})
	}
}

func TestParseDockerfileLabels_NonExistent(t *testing.T) {
	labels := parseDockerfileLabels("/nonexistent/Dockerfile")
	assert.Empty(t, labels)
}

func TestDetectImageName_FromDockerfileLabels(t *testing.T) {
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	err := os.WriteFile(dockerfile, []byte(`FROM alpine
LABEL gordon.domain="myapp.example.com"
`), 0644)
	assert.NoError(t, err)

	name, err := detectImageName(dockerfile)
	assert.NoError(t, err)
	assert.Equal(t, "myapp.example.com", name)
}

func TestDetectImageName_FallbackToDir(t *testing.T) {
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	err := os.WriteFile(dockerfile, []byte(`FROM alpine
`), 0644)
	assert.NoError(t, err)

	// When no gordon.domain label, falls back to cwd basename
	name, err := detectImageName(dockerfile)
	assert.NoError(t, err)
	assert.NotEmpty(t, name)
}

func TestSplitLabelPairs(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:     "single pair",
			content:  `gordon.domain="test.com"`,
			expected: []string{`gordon.domain="test.com"`},
		},
		{
			name:     "two pairs",
			content:  `gordon.domain="test.com" gordon.port="8080"`,
			expected: []string{`gordon.domain="test.com"`, `gordon.port="8080"`},
		},
		{
			name:     "unquoted",
			content:  `gordon.domain=test.com gordon.port=8080`,
			expected: []string{`gordon.domain=test.com`, `gordon.port=8080`},
		},
		{
			name:     "quoted with spaces in value",
			content:  `gordon.domain="my app.com"`,
			expected: []string{`gordon.domain="my app.com"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := splitLabelPairs(tt.content)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseLabelPair(t *testing.T) {
	tests := []struct {
		name      string
		pair      string
		wantKey   string
		wantValue string
		wantOk    bool
	}{
		{"quoted", `gordon.domain="test.com"`, "gordon.domain", "test.com", true},
		{"unquoted", `gordon.domain=test.com`, "gordon.domain", "test.com", true},
		{"single quoted", `gordon.domain='test.com'`, "gordon.domain", "test.com", true},
		{"empty value", `gordon.domain=`, "gordon.domain", "", true},
		{"no equals", `gordon.domain`, "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, ok := parseLabelPair(tt.pair)
			assert.Equal(t, tt.wantOk, ok)
			if ok {
				assert.Equal(t, tt.wantKey, key)
				assert.Equal(t, tt.wantValue, value)
			}
		})
	}
}
