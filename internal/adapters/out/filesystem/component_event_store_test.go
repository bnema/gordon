package filesystem

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestComponentEventStoreParentSyncFailureIsReportedAndDurableDataRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "component-events.json")
	store, err := NewComponentEventStore(path, 2)
	require.NoError(t, err)
	calls := 0
	store.syncDir = func(string) error {
		calls++
		return context.DeadlineExceeded
	}
	err = store.MarkComponentEventProcessed(t.Context(), "durable-before-sync-error", time.Now())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, calls)

	// Rename completed before the injected directory-sync error. A restart must
	// recover the complete acknowledgement rather than a partial file.
	restarted, err := NewComponentEventStore(path, 2)
	require.NoError(t, err)
	processed, err := restarted.IsComponentEventProcessed(t.Context(), "durable-before-sync-error")
	require.NoError(t, err)
	require.True(t, processed)
}

func TestComponentEventStorePersistsBoundedAcknowledgementsAndIntents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "component-events.json")
	store, err := NewComponentEventStore(path, 2)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, store.MarkComponentEventProcessed(ctx, "old", time.Unix(1, 0)))
	require.NoError(t, store.MarkComponentEventProcessed(ctx, "middle", time.Unix(2, 0)))
	require.NoError(t, store.MarkComponentEventProcessed(ctx, "new", time.Unix(3, 0)))
	require.NoError(t, store.SaveManualDeploymentIntents(ctx, map[string]time.Time{"app:v1": time.Now().Add(time.Minute)}))

	restarted, err := NewComponentEventStore(path, 2)
	require.NoError(t, err)
	old, err := restarted.IsComponentEventProcessed(ctx, "old")
	require.NoError(t, err)
	require.False(t, old)
	processed, err := restarted.IsComponentEventProcessed(ctx, "new")
	require.NoError(t, err)
	require.True(t, processed)
	intents, err := restarted.LoadManualDeploymentIntents(ctx)
	require.NoError(t, err)
	require.Contains(t, intents, "app:v1")
}
