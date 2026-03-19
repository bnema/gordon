package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewStore_SaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	store := NewPreviewStore(filepath.Join(dir, "previews.json"))

	previews := []domain.PreviewRoute{
		{
			Domain:     "myapp--feat.example.com",
			Image:      "myapp:preview-feat",
			BaseRoute:  "myapp.example.com",
			Name:       "feat",
			CreatedAt:  time.Now().Truncate(time.Second),
			ExpiresAt:  time.Now().Add(48 * time.Hour).Truncate(time.Second),
			HTTPS:      true,
			Volumes:    []string{"preview-feat-pgdata"},
			Containers: []string{"preview-feat-postgres"},
		},
	}

	ctx := context.Background()
	err := store.Save(ctx, previews)
	require.NoError(t, err)

	loaded, err := store.Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, previews, loaded)
}

func TestPreviewStore_LoadEmpty(t *testing.T) {
	dir := t.TempDir()
	store := NewPreviewStore(filepath.Join(dir, "previews.json"))

	loaded, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

func TestPreviewStore_SaveEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "previews.json")
	store := NewPreviewStore(path)

	err := store.Save(context.Background(), []domain.PreviewRoute{})
	require.NoError(t, err)

	_, err = os.Stat(path)
	assert.True(t, os.IsNotExist(err))
}
