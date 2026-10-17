package app

import (
	"context"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
	cronSvc "github.com/bnema/gordon/internal/usecase/cron"
)

type fakeNextRunObserver struct {
	got map[domain.BackupKind]func() time.Time
}

func (f *fakeNextRunObserver) ObserveBackupNextRun(kind domain.BackupKind, next func() time.Time) {
	if f.got == nil {
		f.got = map[domain.BackupKind]func() time.Time{}
	}
	f.got[kind] = next
}

func TestObserveSchedulerNextRun_ReadsTheLiveEntry(t *testing.T) {
	scheduler := cronSvc.NewScheduler(zerowrap.Default())
	require.NoError(t, scheduler.Add("volume-backup-scheduler", "Volume Backups",
		domain.CronSchedule{Preset: domain.ScheduleDaily}, func(context.Context) error { return nil }))

	obs := &fakeNextRunObserver{}
	observeSchedulerNextRun(obs, domain.BackupKindVolume, scheduler, "volume-backup-scheduler")

	require.Contains(t, obs.got, domain.BackupKindVolume)
	next := obs.got[domain.BackupKindVolume]()
	assert.Equal(t, nextRunOf(scheduler, "volume-backup-scheduler"), next)
	assert.True(t, next.After(time.Now()))
}

func TestObserveSchedulerNextRun_NilSafe(t *testing.T) {
	scheduler := cronSvc.NewScheduler(zerowrap.Default())
	assert.NotPanics(t, func() {
		observeSchedulerNextRun(nil, domain.BackupKindVolume, scheduler, "x")
	})
	obs := &fakeNextRunObserver{}
	observeSchedulerNextRun(obs, domain.BackupKindVolume, nil, "x")
	assert.Empty(t, obs.got)
}
