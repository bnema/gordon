package cli

import (
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
	args := buildImageArgs("v1.0.0", "linux/amd64", []string{"CGO_ENABLED=0"}, "reg.example.com/app:v1.0.0", "reg.example.com/app:latest")

	assert.Contains(t, args, "--load")
	assert.NotContains(t, args, "--push")
	assert.Contains(t, args, "--platform")
}
