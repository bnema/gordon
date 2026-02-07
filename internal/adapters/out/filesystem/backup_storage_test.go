package filesystem

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupStorage_StoreAndGet(t *testing.T) {
	storage, err := NewBackupStorage(t.TempDir(), testLogger())
	require.NoError(t, err)

	now := time.Date(2026, 2, 7, 11, 0, 0, 0, time.UTC)
	payload := []byte("backup-content")
	path, err := storage.Store(context.Background(), "app.example.com", "postgres", domain.ScheduleDaily, now, bytes.NewReader(payload))
	require.NoError(t, err)

	rc, err := storage.Get(context.Background(), path)
	require.NoError(t, err)
	defer rc.Close()

	data := make([]byte, len(payload))
	_, err = rc.Read(data)
	require.NoError(t, err)
	assert.Equal(t, payload, data)
}

func TestBackupStorage_ListWithScheduleFilter(t *testing.T) {
	storage, err := NewBackupStorage(t.TempDir(), testLogger())
	require.NoError(t, err)

	base := time.Date(2026, 2, 7, 10, 0, 0, 0, time.UTC)
	_, err = storage.Store(context.Background(), "app.example.com", "postgres", domain.ScheduleDaily, base, bytes.NewReader([]byte("d1")))
	require.NoError(t, err)
	_, err = storage.Store(context.Background(), "app.example.com", "postgres", domain.ScheduleDaily, base.Add(time.Hour), bytes.NewReader([]byte("d2")))
	require.NoError(t, err)
	_, err = storage.Store(context.Background(), "app.example.com", "postgres", domain.ScheduleWeekly, base.Add(2*time.Hour), bytes.NewReader([]byte("w1")))
	require.NoError(t, err)

	schedule := domain.ScheduleDaily
	jobs, err := storage.List(context.Background(), "app.example.com", &schedule)
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	assert.Equal(t, domain.ScheduleDaily, jobs[0].Schedule)
	assert.Equal(t, domain.ScheduleDaily, jobs[1].Schedule)
	assert.True(t, jobs[0].StartedAt.After(jobs[1].StartedAt))
}

func TestBackupStorage_Delete(t *testing.T) {
	storage, err := NewBackupStorage(t.TempDir(), testLogger())
	require.NoError(t, err)

	path, err := storage.Store(context.Background(), "app.example.com", "postgres", domain.ScheduleDaily, time.Now().UTC(), bytes.NewReader([]byte("data")))
	require.NoError(t, err)

	err = storage.Delete(context.Background(), path)
	require.NoError(t, err)

	_, err = storage.Get(context.Background(), path)
	assert.Error(t, err)
}

func TestBackupStorage_ApplyRetention(t *testing.T) {
	storage, err := NewBackupStorage(t.TempDir(), testLogger())
	require.NoError(t, err)

	base := time.Date(2026, 2, 7, 6, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		_, err := storage.Store(context.Background(), "app.example.com", "postgres", domain.ScheduleDaily, base.Add(time.Duration(i)*time.Hour), bytes.NewReader([]byte("data")))
		require.NoError(t, err)
	}

	deleted, err := storage.ApplyRetention(context.Background(), "app.example.com", domain.RetentionPolicy{Daily: 2})
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)

	schedule := domain.ScheduleDaily
	jobs, err := storage.List(context.Background(), "app.example.com", &schedule)
	require.NoError(t, err)
	assert.Len(t, jobs, 2)
}
