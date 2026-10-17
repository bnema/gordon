package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/domain"
	cronSvc "github.com/bnema/gordon/internal/usecase/cron"
)

func startOptionalSchedulers(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger, v *viper.Viper) (func(), error) {
	schedulers := make([]*cronSvc.Scheduler, 0, 3)
	stopAll := func() {
		for i := len(schedulers) - 1; i >= 0; i-- {
			schedulers[i].Stop()
		}
	}

	backupScheduler, err := startBackupScheduler(ctx, cfg, svc, log)
	if err != nil {
		return nil, err
	}
	if backupScheduler != nil {
		schedulers = append(schedulers, backupScheduler)
	}

	volumeBackupScheduler, err := startVolumeBackupScheduler(ctx, cfg, svc, log)
	if err != nil {
		stopAll()
		return nil, err
	}
	if volumeBackupScheduler != nil {
		schedulers = append(schedulers, volumeBackupScheduler)
	}

	imageScheduler, err := startImagePruneScheduler(ctx, cfg, svc, log, func() int {
		return v.GetInt("images.prune.keep_last")
	})
	if err != nil {
		stopAll()
		return nil, err
	}
	if imageScheduler != nil {
		schedulers = append(schedulers, imageScheduler)
	}

	if len(schedulers) == 0 {
		return nil, nil
	}

	return stopAll, nil
}

func startBackupScheduler(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger) (*cronSvc.Scheduler, error) {
	dbCfg := databaseBackupSettings(cfg)
	if !dbCfg.Enabled || svc == nil || svc.backupSvc == nil {
		return nil, nil
	}

	schedule, err := resolveCronSchedule(dbCfg.Schedule, dbCfg.Time, "backups.databases")
	if err != nil {
		return nil, err
	}
	preset := schedule.Preset

	scheduler := cronSvc.NewScheduler(log)
	err = scheduler.Add(
		"backup-scheduler",
		"Backups",
		schedule,
		func(jobCtx context.Context) error {
			if err := svc.backupSvc.RunForSchedule(jobCtx, preset); err != nil {
				return err
			}
			log.Info().
				Str("schedule", describeSchedule(schedule)).
				Msg("scheduled backup run complete")
			return nil
		},
	)
	if err != nil {
		return nil, log.WrapErr(err, "failed to register backup schedule")
	}

	scheduler.Start(ctx)
	log.Info().
		Str("schedule", describeSchedule(schedule)).
		Time("next_run", nextRunOf(scheduler, "backup-scheduler")).
		Msg("backup scheduler enabled")

	return scheduler, nil
}

// nextRunOf returns the next planned run of one registered job.
func nextRunOf(scheduler *cronSvc.Scheduler, id string) time.Time {
	for _, entry := range scheduler.List() {
		if entry.ID == id {
			return entry.NextRun
		}
	}
	return time.Time{}
}

func startVolumeBackupScheduler(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger) (*cronSvc.Scheduler, error) {
	if !cfg.Backups.Volumes.Enabled || svc == nil || svc.volumeBackupSvc == nil {
		return nil, nil
	}

	volumeCfg, err := validateVolumeBackupConfig(cfg)
	if err != nil {
		return nil, err
	}

	scheduler := cronSvc.NewScheduler(log)
	err = scheduler.Add(
		"volume-backup-scheduler",
		"Volume Backups",
		volumeCfg.Schedule,
		func(jobCtx context.Context) error {
			if err := svc.volumeBackupSvc.RunVolumeBackupsForSchedule(jobCtx, volumeCfg.Schedule.Preset); err != nil {
				return err
			}
			log.Info().
				Str("schedule", describeSchedule(volumeCfg.Schedule)).
				Msg("scheduled volume backup run complete")
			return nil
		},
	)
	if err != nil {
		return nil, log.WrapErr(err, "failed to register volume backup schedule")
	}

	scheduler.Start(ctx)
	log.Info().
		Str("schedule", describeSchedule(volumeCfg.Schedule)).
		Time("next_run", nextRunOf(scheduler, "volume-backup-scheduler")).
		Msg("volume backup scheduler enabled")

	return scheduler, nil
}

func startImagePruneScheduler(ctx context.Context, cfg Config, svc *services, log zerowrap.Logger, keepLastGetter func() int) (*cronSvc.Scheduler, error) {
	if !cfg.Images.Prune.Enabled || svc == nil || svc.imageSvc == nil {
		return nil, nil
	}
	// The scheduler runs the same use case and planner as manual
	// execution. App presence is normal: apps exist on every real
	// server, and prune decides per candidate.
	if keepLastGetter == nil {
		keepLastGetter = func() int { return cfg.Images.Prune.KeepLast }
	}
	if keepLastGetter() < 0 {
		return nil, fmt.Errorf("images.prune.keep_last must be >= 0")
	}

	preset, err := resolveImagePruneSchedule(cfg.Images.Prune.Schedule)
	if err != nil {
		return nil, err
	}

	scheduler := cronSvc.NewScheduler(log)
	err = scheduler.Add(
		"image-prune",
		"Image prune",
		domain.CronSchedule{Preset: preset},
		func(jobCtx context.Context) error {
			keepLast := keepLastGetter()
			if keepLast < 0 {
				log.Warn().
					Int("configured_keep_last", keepLast).
					Int("fallback_keep_last", domain.DefaultImagePruneKeepLast).
					Msg("invalid images.prune.keep_last; using default")
				keepLast = domain.DefaultImagePruneKeepLast
			}

			report, err := svc.imageSvc.Prune(jobCtx, domain.ImagePruneOptions{
				KeepLast:      keepLast,
				PruneDangling: true,
				PruneRegistry: true,
			})
			if err != nil {
				return err
			}

			log.Info().
				Int("keep_last", keepLast).
				Int("eligible", report.Plan.CountByVerdict(domain.PruneVerdictEligible)).
				Int("protected", report.Plan.CountByVerdict(domain.PruneVerdictProtected)).
				Int("unknown", report.Plan.CountByVerdict(domain.PruneVerdictUnknown)).
				Int("deleted", len(report.Plan.Deleted)).
				Int("failures", len(report.Plan.Failures)).
				Int("inventory_gaps", len(report.Plan.Gaps)).
				Int("runtime_deleted", report.Runtime.DeletedCount).
				Int("registry_tags_removed", report.Registry.TagsRemoved).
				Int("registry_blobs_removed", report.Registry.BlobsRemoved).
				Msg("scheduled image prune complete")
			return nil
		},
	)
	if err != nil {
		return nil, log.WrapErr(err, "failed to register image prune schedule")
	}

	scheduler.Start(ctx)
	log.Info().
		Str("schedule", string(preset)).
		Int("keep_last", keepLastGetter()).
		Msg("image prune scheduler enabled")

	return scheduler, nil
}

func resolveImagePruneSchedule(raw string) (domain.BackupSchedule, error) {
	return resolveSchedulePreset(raw, "images.prune.schedule", domain.ScheduleDaily)
}

// resolveCronSchedule builds a backup schedule from a preset and an
// optional HH:MM time read in the server's local timezone. prefix names
// the config section (e.g. "backups.volumes") for error messages.
func resolveCronSchedule(rawSchedule, rawTime, prefix string) (domain.CronSchedule, error) {
	preset, err := resolveSchedulePreset(rawSchedule, prefix+".schedule", domain.ScheduleDaily)
	if err != nil {
		return domain.CronSchedule{}, err
	}
	schedule := domain.CronSchedule{Preset: preset, Location: time.Local}
	if strings.TrimSpace(rawTime) == "" {
		return schedule, nil
	}
	at, err := domain.ParseTimeOfDay(rawTime)
	if err != nil {
		return domain.CronSchedule{}, fmt.Errorf("%s.time: %w", prefix, err)
	}
	schedule.At = &at
	return schedule, nil
}

// describeSchedule renders a schedule for logs, e.g. "daily at 03:30 Europe/Paris".
func describeSchedule(s domain.CronSchedule) string {
	at := "default time"
	if s.At != nil {
		at = s.At.String()
	}
	loc := "UTC"
	if s.Location != nil {
		// time.Local reports "Local"; the current zone abbreviation is
		// what an operator can read (e.g. CEST).
		loc, _ = time.Now().In(s.Location).Zone()
	}
	return fmt.Sprintf("%s at %s %s", s.Preset, at, loc)
}

func resolveSchedulePreset(raw, name string, defaultVal domain.BackupSchedule) (domain.BackupSchedule, error) {
	schedule := domain.BackupSchedule(strings.ToLower(strings.TrimSpace(raw)))
	if schedule == "" {
		schedule = defaultVal
	}

	switch schedule {
	case domain.ScheduleHourly, domain.ScheduleDaily, domain.ScheduleWeekly, domain.ScheduleMonthly:
		return schedule, nil
	default:
		return "", fmt.Errorf("%s must be one of: hourly, daily, weekly, monthly", name)
	}
}
