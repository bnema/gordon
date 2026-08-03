package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestComponentEventStoreRejectsUnsafeOrCorruptLedgerWithoutResettingState(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "oversized", setup: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(strings.Repeat("x", 4096)), 0600))
		}},
		{name: "symlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "target")
			require.NoError(t, os.WriteFile(target, []byte(`{"processed":{},"intents":{}}`), 0600))
			require.NoError(t, os.Symlink(target, path))
		}},
		{name: "fifo", setup: func(t *testing.T, path string) {
			require.NoError(t, syscall.Mkfifo(path, 0600))
		}},
		{name: "hardlink", setup: func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "target")
			require.NoError(t, os.WriteFile(target, []byte(`{"processed":{},"intents":{}}`), 0600))
			require.NoError(t, os.Link(target, path))
		}},
		{name: "corrupt", setup: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"processed":`), 0600))
		}},
		{name: "truncated", setup: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"processed":{"event":"2026-01-01T00:00:00Z"}`), 0600))
		}},
		{name: "permissive", setup: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(`{"processed":{},"intents":{}}`), 0644))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "component-events.json")
			tt.setup(t, path)
			before, err := os.Lstat(path)
			require.NoError(t, err)
			store, err := NewComponentEventStore(path, 2)
			require.Error(t, err)
			require.Nil(t, store)
			after, statErr := os.Lstat(path)
			require.NoError(t, statErr)
			require.Equal(t, before.Mode()&os.ModeType, after.Mode()&os.ModeType)
		})
	}
}

func TestComponentEventStoreRejectsTraversalAndInvalidPayloads(t *testing.T) {
	_, err := NewComponentEventStore(t.TempDir()+"/events/../component-events.json", 1)
	require.Error(t, err)

	path := filepath.Join(t.TempDir(), "component-events.json")
	store, err := NewComponentEventStore(path, 1)
	require.NoError(t, err)
	require.Error(t, store.MarkComponentEventProcessed(t.Context(), strings.Repeat("x", maxComponentEventKeyBytes+1), time.Now()))
	require.Error(t, store.SaveManualDeploymentIntents(t.Context(), map[string]time.Time{"": time.Now()}))
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
