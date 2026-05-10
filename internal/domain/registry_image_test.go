package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
)

func TestRegistryImageKnownGordonRegistryDomains(t *testing.T) {
	got := domain.KnownGordonRegistryDomains(" new-registry.example.com/ ", []string{
		"",
		" old-registry.example.com/ ",
		"old-registry.example.com:5000///",
		"new-registry.example.com",
		"old-registry.example.com",
	})

	want := []string{
		"new-registry.example.com",
		"old-registry.example.com",
		"old-registry.example.com:5000",
	}

	assert.Equal(t, want, got)
}

func TestRegistryImageStripKnownGordonRegistry(t *testing.T) {
	current := "new-registry.example.com/"
	legacy := []string{" old-registry.example.com/ ", "old-registry.example.com:5000/"}

	tests := []struct {
		name     string
		imageRef string
		want     string
	}{
		{name: "current host strip", imageRef: "new-registry.example.com/app:latest", want: "app:latest"},
		{name: "legacy host strip", imageRef: "old-registry.example.com/app:latest", want: "app:latest"},
		{name: "explicit port strip", imageRef: "old-registry.example.com:5000/app:latest", want: "app:latest"},
		{name: "digest ref strip", imageRef: "old-registry.example.com/app@sha256:deadbeef", want: "app@sha256:deadbeef"},
		{name: "bare image pass through", imageRef: "app:latest", want: "app:latest"},
		{name: "external image preserved", imageRef: "docker.io/library/nginx:latest", want: "docker.io/library/nginx:latest"},
		{name: "hostile lookalike preserved", imageRef: "old-registry.example.com.evil/app:latest", want: "old-registry.example.com.evil/app:latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.StripKnownGordonRegistry(tt.imageRef, current, legacy)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegistryImageExtractGordonRepoName(t *testing.T) {
	current := "new-registry.example.com/"
	legacy := []string{"old-registry.example.com/"}

	tests := []struct {
		name     string
		imageRef string
		want     string
	}{
		{name: "current host with tag", imageRef: "new-registry.example.com/app:latest", want: "app"},
		{name: "legacy host with namespace", imageRef: "old-registry.example.com/org/app:v1", want: "org/app"},
		{name: "digest ref", imageRef: "old-registry.example.com/app@sha256:deadbeef", want: "app"},
		{name: "bare image", imageRef: "app:latest", want: "app"},
		{name: "external registry retained in repo name", imageRef: "docker.io/library/nginx:latest", want: "docker.io/library/nginx"},
		{name: "nested external path under gordon registry", imageRef: "old-registry.example.com/docker.io/library/nginx:latest", want: "docker.io/library/nginx"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.ExtractGordonRepoName(tt.imageRef, current, legacy)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegistryImageCanonicalizeGordonImageRef(t *testing.T) {
	legacy := []string{"old-registry.example.com/", "old-registry.example.com:5000/"}

	tests := []struct {
		name     string
		imageRef string
		current  string
		want     string
	}{
		{name: "canonicalization", imageRef: "old-registry.example.com/app:latest", current: "new-registry.example.com/", want: "new-registry.example.com/app:latest"},
		{name: "explicit port canonicalization", imageRef: "old-registry.example.com:5000/app:latest", current: "new-registry.example.com/", want: "new-registry.example.com/app:latest"},
		{name: "external images are not canonicalized", imageRef: "docker.io/library/nginx:latest", current: "new-registry.example.com/", want: "docker.io/library/nginx:latest"},
		{name: "bare refs are not canonicalized", imageRef: "app:latest", current: "new-registry.example.com/", want: "app:latest"},
		{name: "empty current domain leaves ref unchanged", imageRef: "old-registry.example.com/app:latest", current: "", want: "old-registry.example.com/app:latest"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.CanonicalizeGordonImageRef(tt.imageRef, tt.current, legacy)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRegistryImageIsGordonRegistryImageRef(t *testing.T) {
	current := "new-registry.example.com/"
	legacy := []string{"old-registry.example.com/", "old-registry.example.com:5000/"}

	tests := []struct {
		name     string
		imageRef string
		want     bool
	}{
		{name: "current host", imageRef: "new-registry.example.com/app:latest", want: true},
		{name: "legacy host", imageRef: "old-registry.example.com/app:latest", want: true},
		{name: "legacy host with explicit port", imageRef: "old-registry.example.com:5000/app:latest", want: true},
		{name: "bare ref", imageRef: "app:latest", want: false},
		{name: "external ref", imageRef: "docker.io/library/nginx:latest", want: false},
		{name: "hostile lookalike", imageRef: "old-registry.example.com.evil/app:latest", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := domain.IsGordonRegistryImageRef(tt.imageRef, current, legacy)
			assert.Equal(t, tt.want, got)
		})
	}
}
