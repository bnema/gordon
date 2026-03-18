package preview

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
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
	err := svc.Add(t.Context(), p)
	require.NoError(t, err)

	all, err := svc.List(t.Context())
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
	require.NoError(t, svc.Load(t.Context()))

	err := svc.Delete(t.Context(), "feat")
	require.NoError(t, err)

	all, err := svc.List(t.Context())
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
	require.NoError(t, svc.Load(t.Context()))

	p, err := svc.Get(t.Context(), "feat")
	require.NoError(t, err)
	assert.Equal(t, "myapp--feat.example.com", p.Domain)

	_, err = svc.Get(t.Context(), "nonexistent")
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
	require.NoError(t, svc.Load(t.Context()))

	err := svc.Extend(t.Context(), "feat", 24*time.Hour)
	require.NoError(t, err)

	p, _ := svc.Get(t.Context(), "feat")
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
	require.NoError(t, svc.Load(t.Context()))

	expired := svc.GetExpired()
	assert.Len(t, expired, 1)
	assert.Equal(t, "expired", expired[0].Name)
}

func TestPreviewService_Update(t *testing.T) {
	store := &fakeStore{
		previews: []domain.PreviewRoute{
			{Name: "feat", Domain: "myapp--feat.example.com", Status: "deploying"},
		},
	}
	svc := NewService(store, 48*time.Hour)
	require.NoError(t, svc.Load(t.Context()))

	updated := domain.PreviewRoute{
		Name:       "feat",
		Domain:     "myapp--feat.example.com",
		Status:     "running",
		Containers: []string{"gordon-myapp--feat.example.com"},
	}
	err := svc.Update(t.Context(), updated)
	require.NoError(t, err)

	p, err := svc.Get(t.Context(), "feat")
	require.NoError(t, err)
	assert.Equal(t, "running", p.Status)
	assert.Equal(t, []string{"gordon-myapp--feat.example.com"}, p.Containers)
}

func TestPreviewService_Update_NotFound(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, 48*time.Hour)

	err := svc.Update(t.Context(), domain.PreviewRoute{Name: "nonexistent"})
	assert.ErrorIs(t, err, domain.ErrPreviewNotFound)
}
