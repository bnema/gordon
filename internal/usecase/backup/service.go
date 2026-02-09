package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const backupExecTimeout = 30 * time.Minute

// Service orchestrates backup operations.
type Service struct {
	runtime      out.ContainerRuntime
	storage      out.BackupStorage
	containerSvc in.ContainerService
	config       domain.BackupConfig
	log          zerowrap.Logger
}

// NewService creates a backup service.
func NewService(
	runtime out.ContainerRuntime,
	storage out.BackupStorage,
	containerSvc in.ContainerService,
	config domain.BackupConfig,
	log zerowrap.Logger,
) *Service {
	return &Service{
		runtime:      runtime,
		storage:      storage,
		containerSvc: containerSvc,
		config:       config,
		log:          log,
	}
}

// ListBackups returns backups for a domain.
func (s *Service) ListBackups(ctx context.Context, domainName string) ([]domain.BackupJob, error) {
	return s.storage.List(ctx, domainName, nil)
}

// DetectDatabases inspects attachments and returns detected DBs.
func (s *Service) DetectDatabases(ctx context.Context, domainName string) ([]domain.DBInfo, error) {
	attachments := s.containerSvc.ListAttachments(ctx, domainName)
	dbs := make([]domain.DBInfo, 0, len(attachments))
	for _, attachment := range attachments {
		if db, ok := detectDatabaseFromAttachment(domainName, attachment); ok {
			dbs = append(dbs, db)
		}
	}
	return dbs, nil
}

// RunBackup triggers a logical PostgreSQL backup for a detected DB.
func (s *Service) RunBackup(ctx context.Context, domainName, dbName string) (*domain.BackupResult, error) {
	return s.runBackup(ctx, domainName, dbName, domain.BackupSchedule(""))
}

// RunForSchedule executes backups for all detected databases and applies retention.
func (s *Service) RunForSchedule(ctx context.Context, schedule domain.BackupSchedule) error {
	if !isValidBackupSchedule(schedule) {
		return fmt.Errorf("invalid backup schedule: %q", schedule)
	}

	routes := s.containerSvc.List(ctx)
	domainNames := make([]string, 0, len(routes))
	for domainName := range routes {
		domainNames = append(domainNames, domainName)
	}
	sort.Strings(domainNames)

	var firstErr error
	for _, domainName := range domainNames {
		dbs, err := s.DetectDatabases(ctx, domainName)
		if err != nil {
			s.log.Error().Err(err).Str("domain", domainName).Msg("scheduled backup database detection failed")
			if firstErr == nil {
				firstErr = fmt.Errorf("detect databases for %s: %w", domainName, err)
			}
			continue
		}

		for _, db := range dbs {
			if _, err := s.runBackupForDB(ctx, domainName, db, schedule); err != nil {
				s.log.Error().Err(err).Str("domain", domainName).Str("db", db.Name).Msg("scheduled backup failed")
				if firstErr == nil {
					firstErr = fmt.Errorf("run backup for %s/%s: %w", domainName, db.Name, err)
				}
			}
		}

		if _, err := s.storage.ApplyRetention(ctx, domainName, s.config.Retention); err != nil {
			s.log.Error().Err(err).Str("domain", domainName).Msg("scheduled backup retention failed")
			if firstErr == nil {
				firstErr = fmt.Errorf("apply retention for %s: %w", domainName, err)
			}
		}
	}

	return firstErr
}

func (s *Service) runBackup(ctx context.Context, domainName, dbName string, schedule domain.BackupSchedule) (*domain.BackupResult, error) {
	dbs, err := s.DetectDatabases(ctx, domainName)
	if err != nil {
		return nil, err
	}

	db, err := selectDatabase(dbs, dbName)
	if err != nil {
		return nil, err
	}

	return s.runBackupForDB(ctx, domainName, db, schedule)
}

func (s *Service) runBackupForDB(ctx context.Context, domainName string, db domain.DBInfo, schedule domain.BackupSchedule) (*domain.BackupResult, error) {
	started := time.Now().UTC()

	if db.Type != domain.DBTypePostgreSQL {
		return nil, fmt.Errorf("unsupported database type: %s", db.Type)
	}

	execCtx, cancelExec := context.WithTimeout(ctx, backupExecTimeout)
	defer cancelExec()
	dumpPath := fmt.Sprintf("/tmp/gordon-backup-%d.bak", started.UnixNano())
	defer s.cleanupDumpFile(db.ContainerID, dumpPath)

	execResult, err := s.runtime.ExecInContainer(execCtx, db.ContainerID, []string{"sh", "-c", pgDumpToPathCommand(dumpPath, db.Name)})
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("pg_dump timed out after %s", backupExecTimeout)
		}
		return nil, err
	}
	if execResult.ExitCode != 0 {
		return nil, fmt.Errorf("pg_dump failed with exit code %d: %s", execResult.ExitCode, string(execResult.Stderr))
	}

	dumpStream, err := s.runtime.CopyFromContainer(execCtx, db.ContainerID, dumpPath)
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("backup copy timed out after %s", backupExecTimeout)
		}
		return nil, err
	}
	defer dumpStream.Close()

	counter := &byteCounter{}
	path, err := s.storage.Store(ctx, domainName, db.Name, schedule, started, io.TeeReader(dumpStream, counter))
	if err != nil {
		return nil, err
	}

	job := domain.BackupJob{
		ID:          newBackupJobID(started),
		Domain:      domainName,
		DBName:      db.Name,
		Type:        domain.BackupTypeLogical,
		Status:      domain.BackupStatusCompleted,
		StartedAt:   started,
		CompletedAt: time.Now().UTC(),
		SizeBytes:   counter.n,
		FilePath:    path,
	}

	return &domain.BackupResult{
		Job:      job,
		Duration: time.Since(started),
	}, nil
}

func isValidBackupSchedule(schedule domain.BackupSchedule) bool {
	switch schedule {
	case domain.ScheduleHourly, domain.ScheduleDaily, domain.ScheduleWeekly, domain.ScheduleMonthly:
		return true
	default:
		return false
	}
}

// Restore restores a backup by ID.
func (s *Service) Restore(context.Context, string, string) error {
	return fmt.Errorf("backup restore not implemented yet")
}

// RestorePITR restores to a point in time.
func (s *Service) RestorePITR(context.Context, string, time.Time) error {
	return fmt.Errorf("pitr restore not implemented yet")
}

// Status returns aggregate backup status for all managed domains.
func (s *Service) Status(ctx context.Context) ([]domain.BackupJob, error) {
	routes := s.containerSvc.List(ctx)
	domainNames := make([]string, 0, len(routes))
	for domainName := range routes {
		domainNames = append(domainNames, domainName)
	}
	sort.Strings(domainNames)

	const maxWorkers = 4
	sem := make(chan struct{}, maxWorkers)
	results := make([][]domain.BackupJob, len(domainNames))
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for i, domainName := range domainNames {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}

		wg.Add(1)
		go func(idx int, name string) {
			defer wg.Done()
			defer func() { <-sem }()

			domainJobs, err := s.ListBackups(ctx, name)
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			results[idx] = domainJobs
		}(i, domainName)
	}

	wg.Wait()
	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	jobs := make([]domain.BackupJob, 0)
	for _, domainJobs := range results {
		jobs = append(jobs, domainJobs...)
	}

	return jobs, nil
}

func newBackupJobID(started time.Time) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%s-%d", started.Format(time.RFC3339Nano), time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%s", started.Format(time.RFC3339Nano), hex.EncodeToString(random))
}

func selectDatabase(dbs []domain.DBInfo, requested string) (domain.DBInfo, error) {
	if len(dbs) == 0 {
		return domain.DBInfo{}, fmt.Errorf("no supported database attachments detected")
	}

	if requested == "" {
		if len(dbs) == 1 {
			return dbs[0], nil
		}
		return domain.DBInfo{}, fmt.Errorf("multiple database attachments detected; please specify one")
	}

	for _, db := range dbs {
		if strings.EqualFold(db.Name, requested) {
			return db, nil
		}
	}

	return domain.DBInfo{}, fmt.Errorf("database %q not found for domain", requested)
}

func postgresDumpCommand(_ string) string {
	return "pg_dump -Fc --dbname=\"${POSTGRES_DB:-postgres}\" --username=\"${POSTGRES_USER:-postgres}\""
}

func pgDumpToPathCommand(path, dbName string) string {
	return fmt.Sprintf("%s > %q", postgresDumpCommand(dbName), path)
}

func (s *Service) cleanupDumpFile(containerID, dumpPath string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, _ = s.runtime.ExecInContainer(cleanupCtx, containerID, []string{"sh", "-c", fmt.Sprintf("rm -f %q", dumpPath)})
}

type byteCounter struct {
	n int64
}

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
