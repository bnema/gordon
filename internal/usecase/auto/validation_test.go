package auto

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
		{name: "star allows all", domain: "anything.example.com", patterns: []string{"*"}, want: true},
		{name: "empty pattern skipped", domain: "app.example.com", patterns: []string{"", "app.example.com"}, want: true},
		{name: "whitespace trimmed in pattern", domain: "app.example.com", patterns: []string{"  app.example.com  "}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MatchesDomainAllowlist(tt.domain, tt.patterns))
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
		{name: "no tag no digest", imageRef: "myapp", want: "myapp"},
		{name: "empty registry domain", imageRef: "myapp:latest", registryDomain: "", want: "myapp"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractRepoName(tt.imageRef, tt.registryDomain))
		})
	}
}
