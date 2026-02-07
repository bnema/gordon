package backup

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

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
	started := time.Now().UTC()

	dbs, err := s.DetectDatabases(ctx, domainName)
	if err != nil {
		return nil, err
	}

	db, err := selectDatabase(dbs, dbName)
	if err != nil {
		return nil, err
	}

	if db.Type != domain.DBTypePostgreSQL {
		return nil, fmt.Errorf("unsupported database type: %s", db.Type)
	}

	execResult, err := s.runtime.ExecInContainer(ctx, db.ContainerID, []string{"sh", "-c", postgresDumpCommand()})
	if err != nil {
		return nil, err
	}
	if execResult.ExitCode != 0 {
		return nil, fmt.Errorf("pg_dump failed with exit code %d: %s", execResult.ExitCode, string(execResult.Stderr))
	}

	path, err := s.storage.Store(ctx, domainName, db.Name, domain.BackupSchedule(""), started, bytes.NewReader(execResult.Stdout))
	if err != nil {
		return nil, err
	}

	job := domain.BackupJob{
		ID:          started.Format(time.RFC3339Nano),
		Domain:      domainName,
		DBName:      db.Name,
		Type:        domain.BackupTypeLogical,
		Status:      domain.BackupStatusCompleted,
		StartedAt:   started,
		CompletedAt: time.Now().UTC(),
		SizeBytes:   int64(len(execResult.Stdout)),
		FilePath:    path,
	}

	return &domain.BackupResult{
		Job:      job,
		Duration: time.Since(started),
	}, nil
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
	jobs := make([]domain.BackupJob, 0)
	for domainName := range routes {
		domainJobs, err := s.ListBackups(ctx, domainName)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, domainJobs...)
	}
	return jobs, nil
}

func selectDatabase(dbs []domain.DBInfo, requested string) (domain.DBInfo, error) {
	if len(dbs) == 0 {
		return domain.DBInfo{}, fmt.Errorf("no supported database attachments detected")
	}

	if requested == "" {
		return dbs[0], nil
	}

	for _, db := range dbs {
		if strings.EqualFold(db.Name, requested) {
			return db, nil
		}
	}

	return domain.DBInfo{}, fmt.Errorf("database %q not found for domain", requested)
}

func postgresDumpCommand() string {
	return "pg_dump -Fc --dbname=\"${POSTGRES_DB:-postgres}\" --username=\"${POSTGRES_USER:-postgres}\""
}
