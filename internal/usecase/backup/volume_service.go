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

// VolumeService orchestrates volume archive backups.
type VolumeService struct {
	runtime  out.ContainerRuntime
	exporter out.VolumeArchiveExporter
	storage  out.VolumeBackupStorage
	config   domain.VolumeBackupConfig
	log      zerowrap.Logger

	mu     sync.Mutex
	recent map[string]domain.VolumeBackupJob
}

// NewVolumeService creates a volume backup service.
func NewVolumeService(runtime out.ContainerRuntime, exporter out.VolumeArchiveExporter, storage out.VolumeBackupStorage, config domain.VolumeBackupConfig, log zerowrap.Logger) *VolumeService {
	return &VolumeService{
		runtime:  runtime,
		exporter: exporter,
		storage:  storage,
		config:   config,
		log:      log,
		recent:   make(map[string]domain.VolumeBackupJob),
	}
}

// ListVolumeBackups lists completed volume backups for a domain, or all domains when empty.
func (s *VolumeService) ListVolumeBackups(ctx context.Context, domainName string) ([]domain.VolumeBackupJob, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "ListVolumeBackups",
		"domain":              domainName,
	})
	return s.storage.ListVolumeArchives(ctx, domainName)
}

// VolumeBackupStatus returns completed backup artifacts plus current/recent in-memory job state.
func (s *VolumeService) VolumeBackupStatus(ctx context.Context) ([]domain.VolumeBackupJob, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "VolumeBackupStatus",
	})
	jobs, err := s.storage.ListVolumeArchives(ctx, "")
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	for _, job := range s.recent {
		if job.Status == domain.BackupStatusRunning || job.Status == domain.BackupStatusFailed {
			jobs = append(jobs, job)
		}
	}
	s.mu.Unlock()

	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].StartedAt.After(jobs[j].StartedAt)
		}
		return jobs[i].VolumeName < jobs[j].VolumeName
	})
	return jobs, nil
}

// RunVolumeBackups runs volume backups for all eligible targets, optionally scoped to a domain and volume.
func (s *VolumeService) RunVolumeBackups(ctx context.Context, domainName, volumeName string) ([]domain.VolumeBackupJob, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "RunVolumeBackups",
		"domain":              domainName,
		"volume":              volumeName,
	})
	log := zerowrap.FromCtx(ctx)
	if !s.config.Enabled {
		return []domain.VolumeBackupJob{}, nil
	}

	containers, err := s.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("failed to list containers for volume backups: %w", err)
	}
	targets := SelectVolumeBackupTargetsForScope(containers, s.config.VolumePrefix, domainName, volumeName)
	log.Info().
		Str("domain", domainName).
		Str("volume", volumeName).
		Int("targets", len(targets)).
		Msg("selected volume backup targets")
	if len(targets) == 0 {
		return []domain.VolumeBackupJob{}, nil
	}

	results, firstErr := s.runVolumeBackupTargets(ctx, targets)
	if err := s.applyRetentionForSuccessfulVolumeBackups(ctx, results); err != nil && firstErr == nil {
		firstErr = err
	}
	return results, firstErr
}

func (s *VolumeService) runVolumeBackupTargets(ctx context.Context, targets []domain.VolumeBackupTarget) ([]domain.VolumeBackupJob, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, max(s.config.MaxConcurrency, 1))
	results := make([]domain.VolumeBackupJob, 0, len(targets))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

launchLoop:
	for _, target := range targets {
		select {
		case <-runCtx.Done():
			firstErr = runCtx.Err()
			break launchLoop
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(target domain.VolumeBackupTarget) {
			defer wg.Done()
			defer func() { <-sem }()
			job := s.runVolumeBackup(runCtx, target)
			mu.Lock()
			results = append(results, job)
			mu.Unlock()
		}(target)
	}
	if firstErr != nil {
		cancel()
	}
	wg.Wait()
	return results, firstVolumeBackupError(results, firstErr)
}

func (s *VolumeService) applyRetentionForSuccessfulVolumeBackups(ctx context.Context, results []domain.VolumeBackupJob) error {
	successDomains, failedDomains := volumeBackupResultDomains(results)
	for domainName := range successDomains {
		if _, failed := failedDomains[domainName]; failed {
			continue
		}
		deleted, err := s.storage.ApplyVolumeRetention(ctx, domainName, s.config.Retention)
		if err != nil {
			return fmt.Errorf("apply volume backup retention for %s: %w", domainName, err)
		}
		log := zerowrap.FromCtx(ctx)
		log.Info().
			Str("domain", domainName).
			Int("deleted", deleted).
			Msg("volume backup retention applied")
	}
	return nil
}

func firstVolumeBackupError(results []domain.VolumeBackupJob, firstErr error) error {
	if firstErr != nil {
		return firstErr
	}
	for _, job := range results {
		if job.Status == domain.BackupStatusFailed {
			return fmt.Errorf("volume backup failed for %s/%s: %s", job.Domain, job.VolumeName, job.Error)
		}
	}
	return nil
}

func volumeBackupResultDomains(results []domain.VolumeBackupJob) (map[string]struct{}, map[string]struct{}) {
	successDomains := make(map[string]struct{})
	failedDomains := make(map[string]struct{})
	for _, job := range results {
		switch job.Status {
		case domain.BackupStatusCompleted:
			successDomains[job.Domain] = struct{}{}
		case domain.BackupStatusFailed:
			failedDomains[job.Domain] = struct{}{}
		}
	}
	return successDomains, failedDomains
}

func (s *VolumeService) runVolumeBackup(ctx context.Context, target domain.VolumeBackupTarget) domain.VolumeBackupJob {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		"domain":         target.Domain,
		"volume":         target.VolumeName,
		"container_name": target.ContainerName,
		"mount_path":     target.MountPath,
	})
	log := zerowrap.FromCtx(ctx)
	started := time.Now().UTC()
	job := domain.VolumeBackupJob{
		ID:            newBackupJobID(started),
		Domain:        target.Domain,
		ContainerName: target.ContainerName,
		ContainerID:   target.ContainerID,
		VolumeName:    target.VolumeName,
		MountPath:     target.MountPath,
		Type:          domain.BackupTypeVolumeArchive,
		Status:        domain.BackupStatusRunning,
		StartedAt:     started,
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
		VolumeName:  target.VolumeName,
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
	s.recent[job.Domain+"/"+job.VolumeName] = job
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
	delete(s.recent, job.Domain+"/"+job.VolumeName)
}
