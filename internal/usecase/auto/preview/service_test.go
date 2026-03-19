package preview

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	previews []domain.PreviewRoute
}

func (f *fakeStore) Load(_ context.Context) ([]domain.PreviewRoute, error) {
	return f.previews, nil
}
func (f *fakeStore) Save(_ context.Context, p []domain.PreviewRoute) error {
	f.previews = p
	return nil
}

func TestPreviewService_Add(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, 48*time.Hour)

	p := domain.PreviewRoute{
		Domain:    "myapp--feat.example.com",
		Name:      "feat",
		BaseRoute: "myapp.example.com",
		Image:     "myapp:preview-feat",
	}
	err := svc.Add(context.Background(), p)
	require.NoError(t, err)

	all, err := svc.List(context.Background())
	require.NoError(t, err)
	assert.Len(t, all, 1)
	assert.Equal(t, "feat", all[0].Name)
}

func TestPreviewService_Delete(t *testing.T) {
	store := &fakeStore{
		previews: []domain.PreviewRoute{
			{Name: "feat", Domain: "myapp--feat.example.com"},
		},
	}
	svc := NewService(store, 48*time.Hour)
	require.NoError(t, svc.Load(context.Background()))

	err := svc.Delete(context.Background(), "feat")
	require.NoError(t, err)

	all, err := svc.List(context.Background())
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestPreviewService_Get(t *testing.T) {
	store := &fakeStore{
		previews: []domain.PreviewRoute{
			{Name: "feat", Domain: "myapp--feat.example.com"},
		},
	}
	svc := NewService(store, 48*time.Hour)
	require.NoError(t, svc.Load(context.Background()))

	p, err := svc.Get(context.Background(), "feat")
	require.NoError(t, err)
	assert.Equal(t, "myapp--feat.example.com", p.Domain)

	_, err = svc.Get(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestPreviewService_Extend(t *testing.T) {
	now := time.Now()
	store := &fakeStore{
		previews: []domain.PreviewRoute{
			{Name: "feat", ExpiresAt: now.Add(1 * time.Hour)},
		},
	}
	svc := NewService(store, 48*time.Hour)
	require.NoError(t, svc.Load(context.Background()))

	err := svc.Extend(context.Background(), "feat", 24*time.Hour)
	require.NoError(t, err)

	p, _ := svc.Get(context.Background(), "feat")
	assert.True(t, p.ExpiresAt.After(now.Add(23*time.Hour)))
}

func TestPreviewService_GetExpired(t *testing.T) {
	store := &fakeStore{
		previews: []domain.PreviewRoute{
			{Name: "expired", ExpiresAt: time.Now().Add(-1 * time.Hour)},
			{Name: "active", ExpiresAt: time.Now().Add(1 * time.Hour)},
		},
	}
	svc := NewService(store, 48*time.Hour)
	require.NoError(t, svc.Load(context.Background()))

	expired := svc.GetExpired()
	assert.Len(t, expired, 1)
	assert.Equal(t, "expired", expired[0].Name)
}
