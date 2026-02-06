package cli

import (
	"context"
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

func TestBuildImageArgs(t *testing.T) {
	tests := []struct {
		name         string
		version      string
		platform     string
		buildArgs    []string
		versionRef   string
		latestRef    string
		wantContains []string
	}{
		{
			name:       "basic build with version",
			version:    "v1.0.0",
			platform:   "linux/amd64",
			buildArgs:  nil,
			versionRef: "reg.example.com/app:v1.0.0",
			latestRef:  "reg.example.com/app:latest",
			wantContains: []string{
				"buildx", "build",
				"--platform", "linux/amd64",
				"-t", "reg.example.com/app:latest",
				"-t", "reg.example.com/app:v1.0.0",
				"--build-arg", "VERSION=v1.0.0",
				"--load", ".",
			},
		},
		{
			name:       "build with latest version",
			version:    "latest",
			platform:   "linux/arm64",
			buildArgs:  nil,
			versionRef: "reg.example.com/app:latest",
			latestRef:  "reg.example.com/app:latest",
			wantContains: []string{
				"buildx", "build",
				"--platform", "linux/arm64",
				"-t", "reg.example.com/app:latest",
				"--build-arg", "VERSION=latest",
				"--load", ".",
			},
		},
		{
			name:       "build with custom build args",
			version:    "v2.0.0",
			platform:   "linux/amd64",
			buildArgs:  []string{"CGO_ENABLED=0", "GOOS=linux"},
			versionRef: "reg.example.com/app:v2.0.0",
			latestRef:  "reg.example.com/app:latest",
			wantContains: []string{
				"--build-arg", "CGO_ENABLED=0",
				"--build-arg", "GOOS=linux",
				"--build-arg", "VERSION=v2.0.0",
			},
		},
		{
			name:       "build with multiple platforms notation",
			version:    "v1.0.0",
			platform:   "linux/amd64,linux/arm64",
			buildArgs:  nil,
			versionRef: "reg.example.com/app:v1.0.0",
			latestRef:  "reg.example.com/app:latest",
			wantContains: []string{
				"--platform", "linux/amd64,linux/arm64",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := buildImageArgs(tt.version, tt.platform, tt.buildArgs, tt.versionRef, tt.latestRef)

			for _, want := range tt.wantContains {
				assert.Contains(t, args, want, "expected arg %q to be present", want)
			}
			// Verify --load is always used instead of --push
			assert.Contains(t, args, "--load")
			assert.NotContains(t, args, "--push")
		})
	}
}

func TestDetermineVersion(t *testing.T) {
	tests := []struct {
		name    string
		tag     string
		want    string
		setupFn func() func()
	}{
		{
			name: "explicit tag provided",
			tag:  "v1.2.3",
			want: "v1.2.3",
		},
		{
			name: "empty tag returns latest",
			tag:  "",
			want: "latest",
		},
		{
			name: "tag with special characters",
			tag:  "v1.2.3-beta.1",
			want: "v1.2.3-beta.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			got := determineVersion(ctx, tt.tag)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseImageRef_EdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		image        string
		wantRegistry string
		wantName     string
		wantTag      string
	}{
		{
			name:         "registry with port",
			image:        "reg.example.com:5000/myapp:v1.0.0",
			wantRegistry: "reg.example.com:5000",
			wantName:     "myapp",
			wantTag:      "v1.0.0",
		},
		{
			name:         "registry with port no tag",
			image:        "reg.example.com:5000/myapp",
			wantRegistry: "reg.example.com:5000",
			wantName:     "myapp",
			wantTag:      "latest",
		},
		{
			name:         "nested repository path",
			image:        "reg.example.com/org/team/myapp:v1.0.0",
			wantRegistry: "reg.example.com",
			wantName:     "org/team/myapp",
			wantTag:      "v1.0.0",
		},
		{
			name:         "tag with dashes and dots",
			image:        "reg.example.com/myapp:v1.0.0-beta.1",
			wantRegistry: "reg.example.com",
			wantName:     "myapp",
			wantTag:      "v1.0.0-beta.1",
		},
		{
			name:         "single word image",
			image:        "myapp",
			wantRegistry: "",
			wantName:     "",
			wantTag:      "",
		},
		{
			name:         "empty string",
			image:        "",
			wantRegistry: "",
			wantName:     "",
			wantTag:      "",
		},
		{
			name:         "localhost registry",
			image:        "localhost:5000/myapp:latest",
			wantRegistry: "localhost:5000",
			wantName:     "myapp",
			wantTag:      "latest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, name, tag := parseImageRef(tt.image)
			assert.Equal(t, tt.wantRegistry, registry, "registry mismatch")
			assert.Equal(t, tt.wantName, name, "name mismatch")
			assert.Equal(t, tt.wantTag, tag, "tag mismatch")
		})
	}
}

func TestValidateBuildArg_AdditionalCases(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		wantErr bool
	}{
		{"valid with spaces in value", "KEY=value with spaces", false},
		{"valid with equals in value", "KEY=value=more", false},
		{"valid underscore start", "_PRIVATE=secret", false},
		{"valid mixed case key", "MyVar=value", false},
		{"valid all caps key", "CGO_ENABLED=0", false},
		{"invalid starts with equals", "=value", true},
		{"invalid key with special char", "KEY-NAME=value", true},
		{"invalid key with dot", "MY.KEY=value", true},
		{"invalid no key", "=", true},
		{"invalid space in key", "MY KEY=value", true},
		{"valid key with numbers", "VAR123=value", false},
		{"valid key ending with underscore", "VAR_=value", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBuildArg(tt.arg)
			if tt.wantErr {
				assert.Error(t, err, "expected error for arg %q", tt.arg)
			} else {
				assert.NoError(t, err, "unexpected error for arg %q", tt.arg)
			}
		})
	}
}