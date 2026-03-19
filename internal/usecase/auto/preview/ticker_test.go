package preview

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewService_CleanupExpired(t *testing.T) {
	store := &fakeStore{
		previews: []domain.PreviewRoute{
			{Name: "expired", Domain: "a--expired.example.com", ExpiresAt: time.Now().Add(-1 * time.Hour)},
			{Name: "active", Domain: "a--active.example.com", ExpiresAt: time.Now().Add(24 * time.Hour)},
		},
	}
	svc := NewService(store, 48*time.Hour)
	require.NoError(t, svc.Load(context.Background()))

	expired := svc.CleanupExpired(context.Background())
	assert.Len(t, expired, 1)
	assert.Equal(t, "expired", expired[0].Name)

	all, err := svc.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, all, 1)
	assert.Equal(t, "active", all[0].Name)
}
