package preview

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/bnema/gordon/internal/domain"
)

func TestIsOrphanPreview(t *testing.T) {
	patterns := []string{"preview-*"}
	separator := "--"

	tests := []struct {
		name    string
		c       *domain.Container
		tracked []domain.PreviewRoute
		want    bool
	}{
		{
			name: "orphan preview container",
			c: &domain.Container{
				Created: time.Now().Add(-1 * time.Hour),
				Labels: map[string]string{
					domain.LabelImage:  "myapp:preview-feat",
					domain.LabelDomain: "myapp--feat.example.com",
				},
			},
			tracked: nil,
			want:    true,
		},
		{
			name: "tracked preview is not orphan",
			c: &domain.Container{
				Created: time.Now().Add(-1 * time.Hour),
				Labels: map[string]string{
					domain.LabelImage:  "myapp:preview-feat",
					domain.LabelDomain: "myapp--feat.example.com",
				},
			},
			tracked: []domain.PreviewRoute{
				{Domain: "myapp--feat.example.com"},
			},
			want: false,
		},
		{
			name: "production container — no separator in domain",
			c: &domain.Container{
				Created: time.Now().Add(-1 * time.Hour),
				Labels: map[string]string{
					domain.LabelImage:  "myapp:preview-release",
					domain.LabelDomain: "myapp.example.com",
				},
			},
			tracked: nil,
			want:    false,
		},
		{
			name: "production container — no preview tag",
			c: &domain.Container{
				Created: time.Now().Add(-1 * time.Hour),
				Labels: map[string]string{
					domain.LabelImage:  "myapp:latest",
					domain.LabelDomain: "myapp--feat.example.com",
				},
			},
			tracked: nil,
			want:    false,
		},
		{
			name: "container without gordon labels",
			c: &domain.Container{
				Labels: map[string]string{},
			},
			tracked: nil,
			want:    false,
		},
		{
			name: "container too young — grace period",
			c: &domain.Container{
				Created: time.Now().Add(-2 * time.Minute),
				Labels: map[string]string{
					domain.LabelImage:  "myapp:preview-feat",
					domain.LabelDomain: "myapp--feat.example.com",
				},
			},
			tracked: nil,
			want:    false,
		},
		{
			name: "digest-only image — no tag to match",
			c: &domain.Container{
				Created: time.Now().Add(-1 * time.Hour),
				Labels: map[string]string{
					domain.LabelImage:  "myapp@sha256:abc123",
					domain.LabelDomain: "myapp--feat.example.com",
				},
			},
			tracked: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOrphanPreview(tt.c, tt.tracked, patterns, separator)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsExpiredOrphan(t *testing.T) {
	ttl := 48 * time.Hour

	tests := []struct {
		name    string
		created time.Time
		want    bool
	}{
		{
			name:    "expired — older than TTL",
			created: time.Now().Add(-72 * time.Hour),
			want:    true,
		},
		{
			name:    "not expired — within TTL",
			created: time.Now().Add(-12 * time.Hour),
			want:    false,
		},
		{
			name:    "zero time — unknown, skip to be safe",
			created: time.Time{},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &domain.Container{Created: tt.created}
			got := IsExpiredOrphan(c, ttl)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTagFromImage(t *testing.T) {
	tests := []struct {
		image string
		want  string
	}{
		{"myapp:preview-feat", "preview-feat"},
		{"registry.example.com/myapp:preview-feat", "preview-feat"},
		{"myapp:latest", "latest"},
		{"myapp", ""},
		{"myapp@sha256:abc123", ""},
	}

	for _, tt := range tests {
		t.Run(tt.image, func(t *testing.T) {
			assert.Equal(t, tt.want, tagFromImage(tt.image))
		})
	}
}
