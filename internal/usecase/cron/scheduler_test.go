package cron

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCalculateNextRunPresets(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 34, 20, 0, time.UTC)

	tests := []struct {
		name     string
		schedule domain.BackupSchedule
		expected time.Time
	}{
		{
			name:     "hourly",
			schedule: domain.ScheduleHourly,
			expected: time.Date(2026, 2, 7, 13, 0, 0, 0, time.UTC),
		},
		{
			name:     "daily",
			schedule: domain.ScheduleDaily,
			expected: time.Date(2026, 2, 8, 2, 0, 0, 0, time.UTC),
		},
		{
			name:     "weekly",
			schedule: domain.ScheduleWeekly,
			expected: time.Date(2026, 2, 8, 3, 0, 0, 0, time.UTC),
		},
		{
			name:     "monthly",
			schedule: domain.ScheduleMonthly,
			expected: time.Date(2026, 3, 1, 4, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, err := calculateNextRun(now, domain.CronSchedule{Preset: tt.schedule})
			require.NoError(t, err)
			assert.Equal(t, tt.expected, next)
		})
	}
}

func TestSchedulerAddListAndRunNow(t *testing.T) {
	s := NewScheduler(zerowrap.Default())
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }

	runs := 0
	err := s.Add("backup-daily", "daily backup", domain.CronSchedule{Preset: domain.ScheduleDaily}, func(context.Context) error {
		runs++
		return nil
	})
	require.NoError(t, err)

	entries := s.List()
	require.Len(t, entries, 1)
	assert.Equal(t, "backup-daily", entries[0].ID)
	assert.Equal(t, "daily backup", entries[0].Name)

	err = s.RunNow(context.Background(), "backup-daily")
	require.NoError(t, err)
	assert.Equal(t, 1, runs)

	entries = s.List()
	require.Len(t, entries, 1)
	assert.Equal(t, now, entries[0].LastRun)
}

func TestSchedulerRunNowRejectsConcurrentRun(t *testing.T) {
	s := NewScheduler(zerowrap.Default())

	started := make(chan struct{})
	release := make(chan struct{})
	err := s.Add("backup-hourly", "hourly backup", domain.CronSchedule{Preset: domain.ScheduleHourly}, func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	require.NoError(t, err)

	var wg sync.WaitGroup
	firstErrCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		firstErrCh <- s.RunNow(context.Background(), "backup-hourly")
	}()

	<-started
	err2 := s.RunNow(context.Background(), "backup-hourly")
	assert.Error(t, err2)

	close(release)
	wg.Wait()
	err1 := <-firstErrCh
	require.NoError(t, err1)
}
