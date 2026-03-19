package preview

import (
	"testing"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
)

func TestAutoPreviewHandler_CanHandle(t *testing.T) {
	h := &AutoPreviewHandler{}
	assert.True(t, h.CanHandle(domain.EventImagePushed))
	assert.False(t, h.CanHandle(domain.EventConfigReload))
}

func TestCollectDomains(t *testing.T) {
	tests := []struct {
		name   string
		labels *domain.ImageLabels
		want   []string
	}{
		{"single domain", &domain.ImageLabels{Domain: "app.example.com"}, []string{"app.example.com"}},
		{"multiple domains", &domain.ImageLabels{Domains: []string{"a.com", "b.com"}}, []string{"a.com", "b.com"}},
		{"both", &domain.ImageLabels{Domain: "main.com", Domains: []string{"alt.com"}}, []string{"main.com", "alt.com"}},
		{"empty", &domain.ImageLabels{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, collectDomains(tt.labels))
		})
	}
}
