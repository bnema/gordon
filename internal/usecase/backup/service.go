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
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const backupExecTimeout = 30 * time.Minute

// Service orchestrates declarative PostgreSQL app backups. Targets come
// from the app's ACTIVE record: an app declares its databases and which
// of them are backed up, and the app name is the backup identity.
type Service struct {
	runtime out.ContainerRuntime
	storage out.BackupStorage
	config  domain.BackupConfig
	log     zerowrap.Logger
	state   out.AppStateReader
}

// NewService creates a backup service.
func NewService(
	runtime out.ContainerRuntime,
	storage out.BackupStorage,
	config domain.BackupConfig,
	log zerowrap.Logger,
) *Service {
	return &Service{
		runtime: runtime,
		storage: storage,
		config:  config,
		log:     log,
	}
}

// WithAppState wires the ACTIVE app state target resolution reads.
// Without it no target can be resolved.
func (s *Service) WithAppState(state out.AppStateReader) *Service {
	s.state = state
	return s
}

// Targets returns every declared database target of one app, ordered by
// service then database.
func (s *Service) Targets(ctx context.Context, app string) ([]domain.DatabaseTarget, error) {
	if s.state == nil {
		return nil, fmt.Errorf("backup: app state is not wired")
	}
	targets, _, err := declaredTargets(ctx, s.state, app)
	if err != nil {
		return nil, err
	}
	return targets, nil
}

// allTargets returns the declared database targets of every app.
func (s *Service) allTargets(ctx context.Context) ([]domain.DatabaseTarget, error) {
	if s.state == nil {
		return nil, fmt.Errorf("backup: app state is not wired")
	}
	apps, err := appNames(ctx, s.state)
	if err != nil {
		return nil, err
	}
	var targets []domain.DatabaseTarget
	for _, app := range apps {
		appTargets, err := s.Targets(ctx, app)
		if err != nil {
			return nil, err
		}
		targets = append(targets, appTargets...)
	}
	return targets, nil
}

// RunBackup runs one declared database backup. The service and database
// selectors are explicit; an omitted selector only succeeds when exactly
// one compatible target exists.
func (s *Service) RunBackup(ctx context.Context, app, service, database string) (*domain.BackupResult, error) {
	targets, err := s.Targets(ctx, app)
	if err != nil {
		return nil, err
	}
	target, err := selectTarget(targets, "database", service, database, databaseTargetKey)
	if err != nil {
		return nil, err
	}
	return s.runTarget(ctx, target, "")
}

// RunForSchedule runs every declared database whose own schedule matches,
// then applies retention under the canonical app identity. Apps and
// databases that do not declare this schedule are never touched.
func (s *Service) RunForSchedule(ctx context.Context, schedule domain.BackupSchedule) error {
	if !isValidBackupSchedule(schedule) {
		return fmt.Errorf("invalid backup schedule: %q", schedule)
	}
	targets, err := s.allTargets(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	apps := map[string]struct{}{}
	for _, target := range targets {
		if target.Schedule != schedule {
			continue
		}
		if target.ContainerID == "" {
			// Nothing to dump: the declared database has no live
			// container. Reported per target, never guessed.
			s.log.Warn().Str("app", target.App).Str("service", target.Service).
				Str("database", target.Database).Msg("scheduled backup skipped: no active container")
			continue
		}
		if _, err := s.runTarget(ctx, target, schedule); err != nil {
			s.log.Error().Err(err).Str("app", target.App).Str("service", target.Service).
				Str("database", target.Database).Msg("scheduled backup failed")
			if firstErr == nil {
				firstErr = fmt.Errorf("backup %s/%s/%s: %w", target.App, target.Service, target.Database, err)
			}
			continue
		}
		apps[target.App] = struct{}{}
	}
	for _, app := range sortedSet(apps) {
		if _, err := s.storage.ApplyRetention(ctx, app, s.config.Retention); err != nil {
			s.log.Error().Err(err).Str("app", app).Msg("scheduled backup retention failed")
			if firstErr == nil {
				firstErr = fmt.Errorf("apply retention for %s: %w", app, err)
			}
		}
	}
	return firstErr
}

// ListBackups lists stored backups of one app, or of every app when app
// is empty. Storage is keyed by the canonical app name.
func (s *Service) ListBackups(ctx context.Context, app string) ([]domain.BackupJob, error) {
	if app != "" {
		return s.storage.List(ctx, app, nil)
	}
	if s.state == nil {
		return nil, fmt.Errorf("backup: app state is not wired")
	}
	apps, err := appNames(ctx, s.state)
	if err != nil {
		return nil, err
	}
	var jobs []domain.BackupJob
	for _, name := range apps {
		appJobs, err := s.storage.List(ctx, name, nil)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, appJobs...)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].StartedAt.After(jobs[j].StartedAt) })
	return jobs, nil
}

// Status returns stored backups plus the declared targets that have no
// completed backup yet, so an operator can see what is configured and
// what actually ran.
func (s *Service) Status(ctx context.Context) ([]domain.BackupJob, error) {
	jobs, err := s.ListBackups(ctx, "")
	if err != nil {
		return nil, err
	}
	completed := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		completed[job.App+"\x00"+job.Service+"\x00"+job.DBName] = struct{}{}
	}
	targets, err := s.allTargets(ctx)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if _, ok := completed[target.App+"\x00"+target.Service+"\x00"+target.Database]; ok {
			continue
		}
		jobs = append(jobs, domain.BackupJob{
			ID:       "target:" + target.App + "/" + target.Service + "/" + target.Database,
			App:      target.App,
			Service:  target.Service,
			DBName:   target.Database,
			Schedule: target.Schedule,
			Type:     domain.BackupTypeLogical,
			Status:   domain.BackupStatusPending,
			Metadata: map[string]string{"declared_schedule": string(target.Schedule)},
		})
	}
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].StartedAt.Equal(jobs[j].StartedAt) {
			return jobs[i].StartedAt.After(jobs[j].StartedAt)
		}
		return jobs[i].App < jobs[j].App
	})
	return jobs, nil
}

// runTarget runs one declared database backup through the service
// container that hosts it.
func (s *Service) runTarget(ctx context.Context, target domain.DatabaseTarget, schedule domain.BackupSchedule) (*domain.BackupResult, error) {
	if target.ContainerID == "" {
		return nil, fmt.Errorf("backup: app %q service %q has no active container", target.App, target.Service)
	}
	started := time.Now().UTC()

	execCtx, cancelExec := context.WithTimeout(ctx, backupExecTimeout)
	defer cancelExec()
	dumpPath := fmt.Sprintf("/tmp/gordon-backup-%d.bak", started.UnixNano())
	defer s.cleanupDumpFile(target.ContainerID, dumpPath)

	execResult, err := s.runtime.ExecInContainer(execCtx, target.ContainerID, []string{"sh", "-c", pgDumpToPathCommand(dumpPath, target.Database)})
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("pg_dump timed out after %s", backupExecTimeout)
		}
		return nil, err
	}
	if execResult.ExitCode != 0 {
		return nil, fmt.Errorf("pg_dump failed with exit code %d: %s", execResult.ExitCode, string(execResult.Stderr))
	}

	dumpStream, err := s.runtime.CopyFromContainer(execCtx, target.ContainerID, dumpPath)
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("backup copy timed out after %s", backupExecTimeout)
		}
		return nil, err
	}
	defer dumpStream.Close()

	counter := &byteCounter{}
	path, err := s.storage.Store(ctx, target.App, target.Service, target.Database, schedule, started, io.TeeReader(dumpStream, counter))
	if err != nil {
		return nil, err
	}

	job := domain.BackupJob{
		ID:          newBackupJobID(started),
		App:         target.App,
		Service:     target.Service,
		DBName:      target.Database,
		Schedule:    schedule,
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

// databaseTargetKey reports the (service, database) identity of one
// target for selector matching and ambiguity reporting.
func databaseTargetKey(target domain.DatabaseTarget) (string, string) {
	return target.Service, target.Database
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

func newBackupJobID(started time.Time) string {
	random := make([]byte, 4)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("%s-%d", started.Format(time.RFC3339Nano), time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%s", started.Format(time.RFC3339Nano), hex.EncodeToString(random))
}

// pgDumpToPathCommand runs pg_dump inside the declared service container.
// The database name comes from the app declaration (validated as a name),
// while credentials keep coming from the container's own environment: the
// manifest never carries secret values.
func pgDumpToPathCommand(path, dbName string) string {
	return fmt.Sprintf(
		"pg_dump -Fc --dbname=%s --username=\"${POSTGRES_USER:-postgres}\" > %q",
		shellSingleQuote(dbName), path,
	)
}

// shellSingleQuote quotes one validated identifier for a single-quoted
// shell word. Declared names are already restricted to safe characters;
// this keeps that guarantee explicit across shells.
func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func (s *Service) cleanupDumpFile(containerID, dumpPath string) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, _ = s.runtime.ExecInContainer(cleanupCtx, containerID, []string{"sh", "-c", fmt.Sprintf("rm -f %q", dumpPath)})
}

// byteCounter counts the bytes of one stream.
type byteCounter struct {
	n int64
}

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// sortedSet returns the keys of a set in sorted order.
func sortedSet(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
