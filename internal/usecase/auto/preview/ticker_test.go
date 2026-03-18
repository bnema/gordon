package preview

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestPreviewService_CleanupExpired(t *testing.T) {
	store := &fakeStore{
		previews: []domain.PreviewRoute{
			{Name: "expired", Domain: "a--expired.example.com", ExpiresAt: time.Now().Add(-1 * time.Hour)},
			{Name: "active", Domain: "a--active.example.com", ExpiresAt: time.Now().Add(24 * time.Hour)},
		},
	}
	svc := NewService(store, 48*time.Hour)
	require.NoError(t, svc.Load(t.Context()))

	expired := svc.CleanupExpired(t.Context())
	assert.Len(t, expired, 1)
	assert.Equal(t, "expired", expired[0].Name)

	all, err := svc.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, all, 1)
	assert.Equal(t, "active", all[0].Name)
}
