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

	"bnema.dev/zerowrap"

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

	runs *runRegistry[domain.VolumeBackupJob]
}

// NewVolumeService creates a volume backup service.
func NewVolumeService(exporter out.VolumeArchiveExporter, storage out.VolumeBackupStorage, config domain.VolumeBackupConfig, log zerowrap.Logger) *VolumeService {
	return &VolumeService{
		exporter: exporter,
		storage:  storage,
		config:   config,
		log:      log,
		runs:     newRunRegistry(volumeRunMeta),
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

// SeedMetrics reconciles backup metrics with the declared volume targets:
// it forgets targets that are no longer declared and reports the newest
// stored archive of each declared one as its last success, so the metric
// survives a restart. It counts no run.
func (s *VolumeService) SeedMetrics(ctx context.Context) error {
	if s.metrics == nil {
		return nil
	}
	asOf := time.Now()
	targets, err := s.allVolumeTargets(ctx)
	if err != nil {
		return fmt.Errorf("seed volume backup metrics: %w", err)
	}
	refs := make([]domain.BackupTargetRef, 0, len(targets))
	byVolume := make(map[string]domain.BackupTargetRef, len(targets))
	for _, target := range targets {
		ref := volumeTargetRef(target)
		refs = append(refs, ref)
		byVolume[target.App+"/"+target.VolumeName] = ref
	}
	s.metrics.RetainBackupTargets(domain.BackupKindVolume, refs, asOf)

	archives, err := s.storage.LatestVolumeArchives(ctx)
	if err != nil {
		return fmt.Errorf("seed volume backup metrics: %w", err)
	}
	for _, archive := range archives {
		ref, declared := byVolume[archive.App+"/"+archive.VolumeName]
		if !declared {
			continue
		}
		s.metrics.ObserveStoredBackup(domain.BackupRunRecord{
			BackupTargetRef: ref, Succeeded: true, SizeBytes: archive.SizeBytes, StartedAt: archive.StartedAt,
		})
	}
	return nil
}

func volumeTargetRef(target domain.VolumeBackupTarget) domain.BackupTargetRef {
	return domain.BackupTargetRef{Kind: domain.BackupKindVolume, App: target.App, Service: target.Service, Target: target.VolumeName}
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

	jobs = append(jobs, s.runs.unsettled()...)
	sortVolumeJobs(jobs)
	return jobs, nil
}

// StartVolumeBackup starts the declared volume backup of one app and
// returns the running job at once. service and volume are explicit
// selectors: an omitted selector succeeds only when exactly one compatible
// target exists. The archive runs detached from ctx's cancellation, so a
// caller that disconnects never aborts the upload; poll VolumeBackupRun
// with the returned ID for the outcome. When the target already has a
// running backup, that run is returned instead of starting a second one.
func (s *VolumeService) StartVolumeBackup(ctx context.Context, app, service, volume string) (domain.VolumeBackupJob, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "StartVolumeBackup",
		"app":                 app,
		"volume":              volume,
	})
	if !s.config.Enabled {
		return domain.VolumeBackupJob{}, domain.ErrVolumeBackupUnavailable
	}
	targets, err := s.VolumeTargets(ctx, app)
	if err != nil {
		return domain.VolumeBackupJob{}, err
	}
	target, err := selectTarget(targets, "volume", service, volume, volumeTargetKey)
	if err != nil {
		return domain.VolumeBackupJob{}, err
	}
	job, started := s.runs.begin(s.newVolumeJob(target))
	if !started {
		log := zerowrap.FromCtx(ctx)
		log.Info().Str("job_id", job.ID).Msg("volume backup already running")
		return job, nil
	}
	go s.finishVolumeBackup(context.WithoutCancel(ctx), target, job)
	return job, nil
}

// finishVolumeBackup runs one started manual volume backup to its end,
// then applies retention to the app. Its outcome is recorded in the run
// registry; a retention error is logged because no caller waits for it.
func (s *VolumeService) finishVolumeBackup(ctx context.Context, target domain.VolumeBackupTarget, job domain.VolumeBackupJob) {
	job = s.runVolumeBackup(ctx, target, job)
	defer s.runs.put(job)
	if job.Status != domain.BackupStatusCompleted {
		return
	}
	if _, err := s.storage.ApplyVolumeRetention(ctx, job.App, s.config.Retention); err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Error().Err(err).Str("job_id", job.ID).Msg("volume backup retention failed")
	}
}

// VolumeBackupRun returns one tracked volume backup run by ID.
func (s *VolumeService) VolumeBackupRun(_ context.Context, id string) (domain.VolumeBackupJob, error) {
	job, ok := s.runs.get(id)
	if !ok {
		return domain.VolumeBackupJob{}, fmt.Errorf("%w: %s", domain.ErrBackupRunNotFound, id)
	}
	return job, nil
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
			job := s.newVolumeJob(target)
			s.runs.put(job)
			jobs[i] = s.runVolumeBackup(ctx, target, job)
			s.runs.put(jobs[i])
		}(i, target)
	}
	wg.Wait()
	return jobs
}

// runVolumeBackup archives one target and records the outcome in metrics.
// The caller records the final job in the run registry once its own
// follow-up work is done, so a poller never sees a run settle early.
func (s *VolumeService) runVolumeBackup(ctx context.Context, target domain.VolumeBackupTarget, job domain.VolumeBackupJob) domain.VolumeBackupJob {
	job = s.archiveVolume(ctx, target, job)
	if s.metrics != nil {
		s.metrics.RecordBackupRun(ctx, domain.BackupRunRecord{
			BackupTargetRef: volumeTargetRef(target),
			Succeeded:       job.Status == domain.BackupStatusCompleted,
			Duration:        job.CompletedAt.Sub(job.StartedAt),
			SizeBytes:       job.SizeBytes,
			StartedAt:       job.StartedAt,
		})
	}
	return job
}

// newVolumeJob returns the running job of one new archive of target.
func (s *VolumeService) newVolumeJob(target domain.VolumeBackupTarget) domain.VolumeBackupJob {
	return domain.VolumeBackupJob{
		ID:                newVolumeBackupJobID(),
		App:               target.App,
		Service:           target.Service,
		VolumeName:        target.VolumeName,
		RuntimeVolumeName: target.RuntimeVolumeName,
		MountPath:         target.MountPath,
		Type:              domain.BackupTypeVolumeArchive,
		Status:            domain.BackupStatusRunning,
		StartedAt:         time.Now().UTC(),
		Metadata: map[string]string{
			"compression": string(s.config.Compression),
		},
	}
}

func (s *VolumeService) archiveVolume(ctx context.Context, target domain.VolumeBackupTarget, job domain.VolumeBackupJob) domain.VolumeBackupJob {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		"app":            target.App,
		"service":        target.Service,
		"volume":         target.VolumeName,
		"runtime_volume": target.RuntimeVolumeName,
		"mount_path":     target.MountPath,
		"job_id":         job.ID,
	})
	log := zerowrap.FromCtx(ctx)

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

// volumeRunMeta identifies one volume backup run for the run registry.
func volumeRunMeta(job domain.VolumeBackupJob) runMeta {
	return runMeta{
		target: job.App + "\x00" + job.Service + "\x00" + job.VolumeName,
		id:     job.ID,
		status: job.Status,
	}
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
	return job
}

func sortVolumeJobs(jobs []domain.VolumeBackupJob) {
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].StartedAt.After(jobs[j].StartedAt)
		}
		return jobs[i].VolumeName < jobs[j].VolumeName
	})
}
