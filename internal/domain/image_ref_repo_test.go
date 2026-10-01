package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
)

func TestImageRefRepository(t *testing.T) {
	cases := []struct {
		ref  string
		want string
	}{
		{"registry.example.com/blog/web:1.4.2", "blog/web"},
		{"registry.example.com/blog/web@sha256:" + testDigest202[7:], "blog/web"},
		{"blog/web:1.4.2", "blog/web"},
		{"localhost:5000/private/app:1", "private/app"},
		{"127.0.0.1:12345/private@sha256:" + testDigest202[7:], "private"},
		{"nginx:1.25", "nginx"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			assert.Equal(t, tc.want, domain.ImageRefRepository(tc.ref))
		})
	}
}

// TestImageRefRoots_QualifiesDigestWithRepository proves a digest root keeps
// the repository so prune can traverse the manifest's full closure.
func TestImageRefRoots_QualifiesDigestWithRepository(t *testing.T) {
	digest := testDigest202
	roots := domain.ImageRefRoots(domain.ProtectionActiveService, "registry.example.com/blog/web@"+digest, "blog@active.web")

	var digestRoot *domain.ProtectionRoot
	for i := range roots {
		if roots[i].Ref == digest {
			digestRoot = &roots[i]
		}
	}
	if digestRoot == nil {
		t.Fatalf("expected a digest root in %+v", roots)
	}
	assert.Equal(t, "blog/web", digestRoot.Repository)
}
