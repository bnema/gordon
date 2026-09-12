package backup

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// VolumeService orchestrates declarative app volume archive backups.
// Targets come from the app's ACTIVE record: a service declares its
// volumes and which of them are backed up, and the app name is the
// backup identity. No container label or attachment is ever consulted.
type VolumeService struct {
	exporter out.VolumeArchiveExporter
	storage  out.VolumeBackupStorage
	config   domain.VolumeBackupConfig
	log      zerowrap.Logger
	state    out.AppStateReader

	mu     sync.Mutex
	recent map[string]domain.VolumeBackupJob
}

// NewVolumeService creates a volume backup service.
func NewVolumeService(exporter out.VolumeArchiveExporter, storage out.VolumeBackupStorage, config domain.VolumeBackupConfig, log zerowrap.Logger) *VolumeService {
	return &VolumeService{
		exporter: exporter,
		storage:  storage,
		config:   config,
		log:      log,
		recent:   make(map[string]domain.VolumeBackupJob),
	}
}

// WithAppState wires the ACTIVE app state target resolution reads.
func (s *VolumeService) WithAppState(state out.AppStateReader) *VolumeService {
	s.state = state
	return s
}

// VolumeTargets returns every declared volume target of one app.
func (s *VolumeService) VolumeTargets(ctx context.Context, app string) ([]domain.VolumeBackupTarget, error) {
	if s.state == nil {
		return nil, fmt.Errorf("backup: app state is not wired")
	}
	_, targets, err := declaredTargets(ctx, s.state, app)
	if err != nil {
		return nil, err
	}
	return targets, nil
}

// allVolumeTargets returns the declared volume targets of every app.
func (s *VolumeService) allVolumeTargets(ctx context.Context) ([]domain.VolumeBackupTarget, error) {
	if s.state == nil {
		return nil, fmt.Errorf("backup: app state is not wired")
	}
	apps, err := appNames(ctx, s.state)
	if err != nil {
		return nil, err
	}
	var targets []domain.VolumeBackupTarget
	for _, app := range apps {
		appTargets, err := s.VolumeTargets(ctx, app)
		if err != nil {
			return nil, err
		}
		targets = append(targets, appTargets...)
	}
	return targets, nil
}

// ListVolumeBackups lists completed volume backups of one app, or every
// app when app is empty.
func (s *VolumeService) ListVolumeBackups(ctx context.Context, app string) ([]domain.VolumeBackupJob, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListVolumeBackups",
		"app":                 app,
	})
	jobs, err := s.storage.ListVolumeArchives(ctx, app)
	if err != nil {
		return nil, fmt.Errorf("list volume archives: %w", err)
	}
	return jobs, nil
}

// VolumeBackupStatus returns completed backup artifacts plus current or
// recent in-memory jobs.
func (s *VolumeService) VolumeBackupStatus(ctx context.Context) ([]domain.VolumeBackupJob, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "VolumeBackupStatus",
	})
	jobs, err := s.storage.ListVolumeArchives(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list volume backup status: %w", err)
	}

	s.mu.Lock()
	for _, job := range s.recent {
		if job.Status == domain.BackupStatusRunning || job.Status == domain.BackupStatusFailed {
			jobs = append(jobs, job)
		}
	}
	s.mu.Unlock()

	sortVolumeJobs(jobs)
	return jobs, nil
}

// RunVolumeBackups runs the declared volume backups of one app. service
// and volume are explicit selectors: an omitted selector succeeds only
// when exactly one compatible target exists.
func (s *VolumeService) RunVolumeBackups(ctx context.Context, app, service, volume string) ([]domain.VolumeBackupJob, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "RunVolumeBackups",
		"app":                 app,
		"volume":              volume,
	})
	if !s.config.Enabled {
		return []domain.VolumeBackupJob{}, nil
	}
	targets, err := s.VolumeTargets(ctx, app)
	if err != nil {
		return nil, err
	}
	target, err := selectTarget(targets, "volume", service, volume, volumeTargetKey)
	if err != nil {
		return nil, err
	}
	job := s.runVolumeBackup(ctx, target)
	if job.Status == domain.BackupStatusCompleted {
		if _, err := s.storage.ApplyVolumeRetention(ctx, job.App, s.config.Retention); err != nil {
			return []domain.VolumeBackupJob{job}, fmt.Errorf("apply volume backup retention for %s: %w", job.App, err)
		}
	}
	if job.Status == domain.BackupStatusFailed {
		return []domain.VolumeBackupJob{job}, fmt.Errorf("volume backup failed for %s/%s: %s", job.App, job.VolumeName, job.Error)
	}
	return []domain.VolumeBackupJob{job}, nil
}

// RunVolumeBackupsForSchedule runs every declared volume backup of every
// app under the installation's schedule, then applies retention. Volume
// declarations carry no schedule of their own: the installation preset is
// the schedule, and the label is only used for the caller's own filter.
func (s *VolumeService) RunVolumeBackupsForSchedule(ctx context.Context, schedule domain.BackupSchedule) error {
	if !s.config.Enabled {
		return nil
	}
	if schedule != "" && !isValidBackupSchedule(schedule) {
		return fmt.Errorf("invalid backup schedule: %q", schedule)
	}
	targets, err := s.allVolumeTargets(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	apps := map[string]struct{}{}
	for _, target := range targets {
		job := s.runVolumeBackup(ctx, target)
		if job.Status != domain.BackupStatusCompleted {
			if firstErr == nil {
				firstErr = fmt.Errorf("volume backup %s/%s: %s", job.App, job.VolumeName, job.Error)
			}
			continue
		}
		apps[job.App] = struct{}{}
	}
	for _, app := range sortedSet(apps) {
		if _, err := s.storage.ApplyVolumeRetention(ctx, app, s.config.Retention); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("apply volume backup retention for %s: %w", app, err)
			}
		}
	}
	return firstErr
}

func (s *VolumeService) runVolumeBackup(ctx context.Context, target domain.VolumeBackupTarget) domain.VolumeBackupJob {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		"app":            target.App,
		"service":        target.Service,
		"volume":         target.VolumeName,
		"runtime_volume": target.RuntimeVolumeName,
		"mount_path":     target.MountPath,
	})
	log := zerowrap.FromCtx(ctx)
	started := time.Now().UTC()
	job := domain.VolumeBackupJob{
		ID:                newBackupJobID(started),
		App:               target.App,
		Service:           target.Service,
		VolumeName:        target.VolumeName,
		RuntimeVolumeName: target.RuntimeVolumeName,
		MountPath:         target.MountPath,
		Type:              domain.BackupTypeVolumeArchive,
		Status:            domain.BackupStatusRunning,
		StartedAt:         started,
		Metadata: map[string]string{
			"compression": string(s.config.Compression),
		},
	}
	s.remember(job)

	timeout := s.config.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	exportCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	archive, err := s.exporter.ExportVolumeArchive(exportCtx, domain.VolumeArchiveRequest{
		VolumeName:  target.RuntimeVolumeName,
		MountPath:   target.MountPath,
		Compression: s.config.Compression,
		HelperImage: s.config.HelperImage,
	})
	if err != nil {
		log.Error().Err(err).Msg("volume archive export failed")
		return s.failJob(ctx, job, err)
	}
	defer archive.Stream.Close()

	counter := &byteCounter{}
	artifactRef, err := s.storage.StoreVolumeArchive(exportCtx, job, io.TeeReader(archive.Stream, counter))
	if err != nil {
		log.Error().Err(err).Msg("volume archive upload failed")
		return s.failJob(ctx, job, err)
	}

	job.Status = domain.BackupStatusCompleted
	job.CompletedAt = time.Now().UTC()
	job.SizeBytes = counter.n
	job.ArtifactRef = artifactRef
	log.Info().
		Int64("size_bytes", job.SizeBytes).
		Dur("duration", job.CompletedAt.Sub(job.StartedAt)).
		Msg("volume backup completed")
	s.forget(job)
	return job
}

// volumeTargetKey reports the (service, volume) identity of one target.
func volumeTargetKey(target domain.VolumeBackupTarget) (string, string) {
	return target.Service, target.VolumeName
}

func (s *VolumeService) failJob(ctx context.Context, job domain.VolumeBackupJob, err error) domain.VolumeBackupJob {
	job.Status = domain.BackupStatusFailed
	job.CompletedAt = time.Now().UTC()
	job.Error = err.Error()
	log := zerowrap.FromCtx(ctx)
	log.Error().Err(err).Msg("volume backup failed")
	s.remember(job)
	return job
}

func (s *VolumeService) remember(job domain.VolumeBackupJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent[job.App+"/"+job.VolumeName] = job
	if len(s.recent) > 100 {
		for k := range s.recent {
			delete(s.recent, k)
			break
		}
	}
}

func (s *VolumeService) forget(job domain.VolumeBackupJob) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recent, job.App+"/"+job.VolumeName)
}

func sortVolumeJobs(jobs []domain.VolumeBackupJob) {
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].StartedAt.After(jobs[j].StartedAt)
		}
		return jobs[i].VolumeName < jobs[j].VolumeName
	})
}
