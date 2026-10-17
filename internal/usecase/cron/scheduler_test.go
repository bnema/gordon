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

func TestCalculateNextRunAtLocalTime(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)
	at := &domain.TimeOfDay{Hour: 3, Minute: 30}

	tests := []struct {
		name     string
		preset   domain.BackupSchedule
		now      time.Time
		expected time.Time
	}{
		{"daily later today", domain.ScheduleDaily,
			time.Date(2026, 10, 17, 1, 0, 0, 0, paris), time.Date(2026, 10, 17, 3, 30, 0, 0, paris)},
		{"daily already passed", domain.ScheduleDaily,
			time.Date(2026, 10, 17, 7, 24, 0, 0, paris), time.Date(2026, 10, 18, 3, 30, 0, 0, paris)},
		{"daily exactly now runs tomorrow", domain.ScheduleDaily,
			time.Date(2026, 10, 17, 3, 30, 0, 0, paris), time.Date(2026, 10, 18, 3, 30, 0, 0, paris)},
		// 2026-10-25: Paris leaves summer time at 03:00, so 03:30 is CET.
		{"daily across DST end keeps wall clock", domain.ScheduleDaily,
			time.Date(2026, 10, 24, 12, 0, 0, 0, paris), time.Date(2026, 10, 25, 3, 30, 0, 0, paris)},
		{"weekly next sunday", domain.ScheduleWeekly,
			time.Date(2026, 10, 17, 12, 0, 0, 0, paris), time.Date(2026, 10, 18, 3, 30, 0, 0, paris)},
		{"monthly next first day", domain.ScheduleMonthly,
			time.Date(2026, 10, 17, 12, 0, 0, 0, paris), time.Date(2026, 11, 1, 3, 30, 0, 0, paris)},
		{"hourly uses minute only", domain.ScheduleHourly,
			time.Date(2026, 10, 17, 12, 40, 0, 0, paris), time.Date(2026, 10, 17, 13, 30, 0, 0, paris)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, err := calculateNextRun(tt.now, domain.CronSchedule{Preset: tt.preset, At: at, Location: paris})
			require.NoError(t, err)
			assert.True(t, tt.expected.Equal(next), "want %s, got %s", tt.expected, next.In(paris))
		})
	}
}

func TestCalculateNextRunDSTGapSkipsForward(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)
	// 2026-03-29: Paris skips 02:00-03:00. A 02:30 schedule runs once, at
	// the first valid instant after the gap, never twice or never.
	now := time.Date(2026, 3, 28, 12, 0, 0, 0, paris)
	next, err := calculateNextRun(now, domain.CronSchedule{
		Preset: domain.ScheduleDaily, At: &domain.TimeOfDay{Hour: 2, Minute: 30}, Location: paris,
	})
	require.NoError(t, err)
	assert.Equal(t, 2026, next.Year())
	assert.Equal(t, time.March, next.In(paris).Month())
	assert.Equal(t, 29, next.In(paris).Day())
	assert.True(t, next.After(now))
}

func TestCalculateNextRunRejectsMissingPreset(t *testing.T) {
	_, err := calculateNextRun(time.Now(), domain.CronSchedule{})
	require.Error(t, err)
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
	assert.Equal(t, time.Date(2026, 2, 8, 2, 0, 0, 0, time.UTC), entries[0].NextRun)
}

func TestSchedulerWithMaxJobsOption(t *testing.T) {
	s := NewScheduler(zerowrap.Default(), WithMaxJobs(9))
	assert.Equal(t, 9, s.maxJobs)
}

func TestSchedulerRemoveRejectsRunningJob(t *testing.T) {
	s := NewScheduler(zerowrap.Default())

	started := make(chan struct{})
	release := make(chan struct{})
	err := s.Add("backup-hourly", "hourly backup", domain.CronSchedule{Preset: domain.ScheduleHourly}, func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	require.NoError(t, err)

	runErrCh := make(chan error, 1)
	go func() {
		runErrCh <- s.RunNow(context.Background(), "backup-hourly")
	}()

	<-started
	err = s.Remove("backup-hourly")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrJobRunning)

	close(release)
	require.NoError(t, <-runErrCh)
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

func TestSchedulerRunNowRecoversFromPanics(t *testing.T) {
	s := NewScheduler(zerowrap.Default())
	err := s.Add("panic-job", "panic job", domain.CronSchedule{Preset: domain.ScheduleHourly}, func(context.Context) error {
		panic("boom")
	})
	require.NoError(t, err)

	err = s.RunNow(context.Background(), "panic-job")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic")

	entries := s.List()
	require.Len(t, entries, 1)
	assert.False(t, entries[0].Running)
	assert.False(t, entries[0].LastRun.IsZero())
	assert.False(t, entries[0].NextRun.IsZero())
}

func TestSchedulerStartNoOpWhenStopped(t *testing.T) {
	s := NewScheduler(zerowrap.Default())
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }

	runs := 0
	err := s.Add("stopped-job", "stopped job", domain.CronSchedule{Preset: domain.ScheduleDaily}, func(context.Context) error {
		runs++
		return nil
	})
	require.NoError(t, err)

	s.mu.Lock()
	s.entries["stopped-job"].nextRun = now.Add(-time.Minute)
	s.mu.Unlock()

	s.Stop()
	s.Start(context.Background())

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, runs)
	assert.False(t, s.started.Load())
}

func TestSchedulerRunNowComputesNextRunFromJobFinishTime(t *testing.T) {
	s := NewScheduler(zerowrap.Default())
	current := time.Date(2026, 2, 7, 12, 59, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return current }

	err := s.Add("backup-hourly", "hourly backup", domain.CronSchedule{Preset: domain.ScheduleHourly}, func(context.Context) error {
		current = time.Date(2026, 2, 7, 13, 1, 0, 0, time.UTC)
		return nil
	})
	require.NoError(t, err)

	err = s.RunNow(context.Background(), "backup-hourly")
	require.NoError(t, err)

	entries := s.List()
	require.Len(t, entries, 1)
	assert.Equal(t, time.Date(2026, 2, 7, 14, 0, 0, 0, time.UTC), entries[0].NextRun)
}

func TestSchedulerStartNoOpWhenContextAlreadyCanceled(t *testing.T) {
	s := NewScheduler(zerowrap.Default())
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	s.nowFn = func() time.Time { return now }

	runs := 0
	err := s.Add("canceled-job", "canceled job", domain.CronSchedule{Preset: domain.ScheduleDaily}, func(context.Context) error {
		runs++
		return nil
	})
	require.NoError(t, err)

	s.mu.Lock()
	s.entries["canceled-job"].nextRun = now.Add(-time.Minute)
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.Start(ctx)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 0, runs)
	assert.False(t, s.started.Load())
}
