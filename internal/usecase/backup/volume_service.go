package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	metrics  out.BackupMetrics

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

// WithMetrics wires the backup metrics recorder. Without it no metric is
// recorded.
func (s *VolumeService) WithMetrics(metrics out.BackupMetrics) *VolumeService {
	s.metrics = metrics
	return s
}

// SeedMetrics reports the newest stored archive of every target as its
// last success, so the metric survives a restart. It counts no run.
func (s *VolumeService) SeedMetrics(ctx context.Context) error {
	if s.metrics == nil {
		return nil
	}
	jobs, err := s.storage.ListVolumeArchives(ctx, "")
	if err != nil {
		return fmt.Errorf("seed volume backup metrics: %w", err)
	}
	for _, job := range jobs {
		s.metrics.ObserveStoredBackup(domain.BackupRunRecord{
			Kind: domain.BackupKindVolume, App: job.App, Service: job.Service, Target: job.VolumeName,
			Succeeded: true, SizeBytes: job.SizeBytes, FinishedAt: job.StartedAt,
		})
	}
	return nil
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
	jobs := s.runVolumeBackupsConcurrently(ctx, targets)
	var firstErr error
	apps := map[string]struct{}{}
	for _, job := range jobs {
		if job.Status != domain.BackupStatusCompleted {
			if firstErr == nil {
				firstErr = fmt.Errorf("volume backup %s/%s: %s", job.App, job.VolumeName, job.Error)
			}
			continue
		}
		apps[job.App] = struct{}{}
	}
	// Retention runs only after every archive finished, so a pass never
	// deletes an app's older archive while its replacement is uploading.
	for _, app := range sortedSet(apps) {
		if _, err := s.storage.ApplyVolumeRetention(ctx, app, s.config.Retention); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("apply volume backup retention for %s: %w", app, err)
			}
		}
	}
	return firstErr
}

// runVolumeBackupsConcurrently runs every target with at most
// MaxConcurrency exports in flight and returns the jobs in target order.
func (s *VolumeService) runVolumeBackupsConcurrently(ctx context.Context, targets []domain.VolumeBackupTarget) []domain.VolumeBackupJob {
	limit := s.config.MaxConcurrency
	if limit < 1 {
		limit = 1
	}
	jobs := make([]domain.VolumeBackupJob, len(targets))
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, target domain.VolumeBackupTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			jobs[i] = s.runVolumeBackup(ctx, target)
		}(i, target)
	}
	wg.Wait()
	return jobs
}

// runVolumeBackup archives one target and records the outcome in metrics.
func (s *VolumeService) runVolumeBackup(ctx context.Context, target domain.VolumeBackupTarget) domain.VolumeBackupJob {
	job := s.archiveVolume(ctx, target)
	if s.metrics != nil {
		s.metrics.RecordBackupRun(ctx, domain.BackupRunRecord{
			Kind: domain.BackupKindVolume, App: target.App, Service: target.Service, Target: target.VolumeName,
			Succeeded: job.Status == domain.BackupStatusCompleted, Duration: job.CompletedAt.Sub(job.StartedAt),
			SizeBytes: job.SizeBytes, FinishedAt: job.CompletedAt,
		})
	}
	return job
}

func (s *VolumeService) archiveVolume(ctx context.Context, target domain.VolumeBackupTarget) domain.VolumeBackupJob {
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
		ID:                newVolumeBackupJobID(),
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

// newVolumeBackupJobID returns a short random job ID. Storage keys already
// carry the start timestamp, so the ID only has to make each run unique.
func newVolumeBackupJobID() string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(random)
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
