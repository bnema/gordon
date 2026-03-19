package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPreviewRoute_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"expired", time.Now().Add(-1 * time.Hour), true},
		{"not expired", time.Now().Add(1 * time.Hour), false},
		{"just expired", time.Now().Add(-1 * time.Second), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := PreviewRoute{ExpiresAt: tt.expiresAt}
			assert.Equal(t, tt.want, p.IsExpired())
		})
	}
}
