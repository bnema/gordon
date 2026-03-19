package preview

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewLifecycle_Integration(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, 2*time.Second)

	ctx := context.Background()

	// Create
	svc.CreatePreview(ctx, CreatePreviewRequest{
		Name:      "test-feat",
		Domain:    "myapp--test-feat.example.com",
		BaseRoute: "myapp.example.com",
		Image:     "myapp:preview-test-feat",
		HTTPS:     true,
		PreviewConfig: domain.PreviewConfig{
			TTL:       2 * time.Second,
			Separator: "--",
			DataCopy:  false,
		},
	})

	// Verify created
	all, err := svc.List(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "test-feat", all[0].Name)
	assert.Equal(t, "myapp--test-feat.example.com", all[0].Domain)
	assert.Equal(t, "myapp.example.com", all[0].BaseRoute)

	// Extend
	err = svc.Extend(ctx, "test-feat", 1*time.Hour)
	require.NoError(t, err)

	p, err := svc.Get(ctx, "test-feat")
	require.NoError(t, err)
	assert.True(t, p.ExpiresAt.After(time.Now().Add(55*time.Minute)))

	// Delete
	err = svc.Delete(ctx, "test-feat")
	require.NoError(t, err)

	all, err = svc.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)
}

func TestPreviewLifecycle_CleanupExpired(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, 1*time.Millisecond)

	ctx := context.Background()

	// Create with very short TTL
	svc.CreatePreview(ctx, CreatePreviewRequest{
		Name:      "short-lived",
		Domain:    "myapp--short-lived.example.com",
		BaseRoute: "myapp.example.com",
		Image:     "myapp:preview-short-lived",
		HTTPS:     true,
		PreviewConfig: domain.PreviewConfig{
			TTL:       1 * time.Millisecond,
			Separator: "--",
			DataCopy:  false,
		},
	})

	// Wait for expiry
	time.Sleep(10 * time.Millisecond)

	// Cleanup should find it
	expired := svc.CleanupExpired(ctx)
	assert.Len(t, expired, 1)
	assert.Equal(t, "short-lived", expired[0].Name)

	// Should be gone
	all, err := svc.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, all)
}
