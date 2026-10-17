package app

import (
	"context"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/telemetry"
	"github.com/bnema/gordon/internal/domain"
	cronSvc "github.com/bnema/gordon/internal/usecase/cron"
)

// nextRunObserver exports the next planned run of one backup kind.
type nextRunObserver interface {
	ObserveBackupNextRun(kind domain.BackupKind, next func() time.Time)
}

// nextRunObserverPort converts a possibly nil metrics adapter into an
// interface that is nil when metrics are disabled. A typed nil must not
// leak into the interface.
func nextRunObserverPort(metrics *telemetry.Metrics) nextRunObserver {
	if metrics == nil {
		return nil
	}
	return metrics
}

// wireBackupMetrics injects backup metrics into the backup services. It
// must run before any backup can start, so no run reads the field while
// it is written.
func wireBackupMetrics(svc *services) {
	if svc == nil || svc.metrics == nil {
		return
	}
	if svc.backupSvc != nil {
		svc.backupSvc.WithMetrics(svc.metrics)
	}
	if svc.volumeBackupSvc != nil {
		svc.volumeBackupSvc.WithMetrics(svc.metrics)
	}
}

// seedBackupMetrics reports the last success of every target from
// storage, so the metric is correct right after a restart. It only feeds
// the metrics state (mutex-guarded), so it may run alongside backups.
// Failures are logged, never fatal.
func seedBackupMetrics(ctx context.Context, svc *services, log zerowrap.Logger) {
	if svc == nil || svc.metrics == nil {
		return
	}
	if svc.backupSvc != nil {
		if err := svc.backupSvc.SeedMetrics(ctx); err != nil {
			log.Warn().Err(err).Msg("failed to seed database backup metrics")
		}
	}
	if svc.volumeBackupSvc != nil {
		if err := svc.volumeBackupSvc.SeedMetrics(ctx); err != nil {
			log.Warn().Err(err).Msg("failed to seed volume backup metrics")
		}
	}
}

// observeSchedulerNextRun exports the scheduler entry's next run, read at
// each metrics collection.
func observeSchedulerNextRun(obs nextRunObserver, kind domain.BackupKind, scheduler *cronSvc.Scheduler, id string) {
	if obs == nil || scheduler == nil {
		return
	}
	obs.ObserveBackupNextRun(kind, func() time.Time { return nextRunOf(scheduler, id) })
}
