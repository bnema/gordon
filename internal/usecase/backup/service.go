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

	"bnema.dev/zerowrap"

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
	metrics out.BackupMetrics
	runs    *runRegistry[domain.BackupJob]
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
		runs:    newRunRegistry(databaseRunMeta),
	}
}

// WithAppState wires the ACTIVE app state target resolution reads.
// Without it no target can be resolved.
func (s *Service) WithAppState(state out.AppStateReader) *Service {
	s.state = state
	return s
}

// WithMetrics wires the backup metrics recorder. Without it no metric is
// recorded.
func (s *Service) WithMetrics(metrics out.BackupMetrics) *Service {
	s.metrics = metrics
	return s
}

// SeedMetrics reconciles backup metrics with the declared database
// targets: it forgets targets that are no longer declared and reports the
// newest stored dump of each declared one as its last success, so the
// metric survives a restart. It counts no run.
func (s *Service) SeedMetrics(ctx context.Context) error {
	if s.metrics == nil {
		return nil
	}
	asOf := time.Now()
	targets, err := s.allTargets(ctx)
	if err != nil {
		return fmt.Errorf("seed backup metrics: %w", err)
	}
	refs := make([]domain.BackupTargetRef, 0, len(targets))
	declared := make(map[domain.BackupTargetRef]struct{}, len(targets))
	for _, target := range targets {
		ref := databaseTargetRef(target)
		refs = append(refs, ref)
		declared[ref] = struct{}{}
	}
	s.metrics.RetainBackupTargets(domain.BackupKindDatabase, refs, asOf)

	jobs, err := s.ListBackups(ctx, "")
	if err != nil {
		return fmt.Errorf("seed backup metrics: %w", err)
	}
	for _, job := range jobs {
		ref := domain.BackupTargetRef{Kind: domain.BackupKindDatabase, App: job.App, Service: job.Service, Target: job.DBName}
		if _, ok := declared[ref]; !ok {
			continue
		}
		s.metrics.ObserveStoredBackup(domain.BackupRunRecord{
			BackupTargetRef: ref, Succeeded: true, SizeBytes: job.SizeBytes, StartedAt: job.StartedAt,
		})
	}
	return nil
}

func databaseTargetRef(target domain.DatabaseTarget) domain.BackupTargetRef {
	return domain.BackupTargetRef{Kind: domain.BackupKindDatabase, App: target.App, Service: target.Service, Target: target.Database}
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

// StartBackup starts one declared database backup and returns the
// running job at once. The service and database selectors are explicit;
// an omitted selector only succeeds when exactly one compatible target
// exists. The dump runs detached from ctx's cancellation, so a caller that
// disconnects never aborts it; poll BackupRun with the returned ID for the
// outcome. When the target already has a running backup, that run is
// returned instead of starting a second one.
func (s *Service) StartBackup(ctx context.Context, app, service, database string) (domain.BackupJob, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "StartBackup",
		"app":                 app,
	})
	log := zerowrap.FromCtx(ctx)
	targets, err := s.Targets(ctx, app)
	if err != nil {
		return domain.BackupJob{}, err
	}
	target, err := selectTarget(targets, "database", service, database, databaseTargetKey)
	if err != nil {
		return domain.BackupJob{}, err
	}
	if target.ContainerID == "" {
		return domain.BackupJob{}, fmt.Errorf("backup: app %q service %q has no active container", target.App, target.Service)
	}
	job, started := s.runs.begin(newBackupJob(target, ""))
	if !started {
		log.Info().Str("service", job.Service).Str("database", job.DBName).
			Str("job_id", job.ID).Msg("database backup already running")
		return job, nil
	}
	go s.finishBackup(context.WithoutCancel(ctx), target, job)
	return job, nil
}

// finishBackup runs one started manual database backup to its end and
// records the outcome in the run registry.
func (s *Service) finishBackup(ctx context.Context, target domain.DatabaseTarget, job domain.BackupJob) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		"service":  job.Service,
		"database": job.DBName,
		"job_id":   job.ID,
	})
	defer s.failOnPanic(ctx, job)
	job, err := s.runJob(ctx, target, job)
	s.runs.put(job)
	if err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Error().Err(err).Msg("manual backup failed")
	}
}

// failOnPanic records a panic in a detached run as a failed run instead of
// crashing the server. It must be deferred.
func (s *Service) failOnPanic(ctx context.Context, job domain.BackupJob) {
	recovered := recover()
	if recovered == nil {
		return
	}
	job.Status = domain.BackupStatusFailed
	job.CompletedAt = time.Now().UTC()
	job.Error = fmt.Sprintf("panic: %v", recovered)
	s.runs.put(job)
	log := zerowrap.FromCtx(ctx)
	log.Error().Str("panic", job.Error).Msg("manual backup panicked")
}

// BackupRun returns one tracked database backup run by ID.
func (s *Service) BackupRun(_ context.Context, id string) (domain.BackupJob, error) {
	job, ok := s.runs.get(id)
	if !ok {
		return domain.BackupJob{}, fmt.Errorf("%w: %s", domain.ErrBackupRunNotFound, id)
	}
	return job, nil
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
		job, started := s.runs.begin(newBackupJob(target, schedule))
		if !started {
			s.log.Warn().Str("app", target.App).Str("service", target.Service).
				Str("database", target.Database).Str("job_id", job.ID).
				Msg("scheduled backup skipped: a backup of this database is already running")
			continue
		}
		job, err := s.runJob(ctx, target, job)
		s.runs.put(job)
		if err != nil {
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
	// Running and last-failed runs are reported as is; a target with a
	// stored dump or a tracked run is not reported as pending.
	jobs = append(jobs, s.runs.unsettled()...)
	completed := make(map[string]struct{}, len(jobs))
	for _, job := range jobs {
		completed[databaseRunMeta(job).target] = struct{}{}
	}
	targets, err := s.allTargets(ctx)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if _, ok := completed[databaseTargetID(target.App, target.Service, target.Database)]; ok {
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

// newBackupJob returns the running job of one new dump of target.
func newBackupJob(target domain.DatabaseTarget, schedule domain.BackupSchedule) domain.BackupJob {
	started := time.Now().UTC()
	return domain.BackupJob{
		ID:        newBackupJobID(started),
		App:       target.App,
		Service:   target.Service,
		DBName:    target.Database,
		Schedule:  schedule,
		Type:      domain.BackupTypeLogical,
		Status:    domain.BackupStatusRunning,
		StartedAt: started,
	}
}

// databaseTargetID is the registry identity of one database target.
func databaseTargetID(app, service, database string) string {
	return app + "\x00" + service + "\x00" + database
}

// databaseRunMeta identifies one database backup run for the run registry.
func databaseRunMeta(job domain.BackupJob) runMeta {
	return runMeta{
		target: databaseTargetID(job.App, job.Service, job.DBName),
		id:     job.ID,
		status: job.Status,
	}
}

// runJob runs one declared database backup through the service container
// that hosts it, records the outcome in metrics, and returns the settled
// job. The caller records it in the run registry once its own follow-up
// work is done, so a poller never sees a run settle early.
func (s *Service) runJob(ctx context.Context, target domain.DatabaseTarget, job domain.BackupJob) (domain.BackupJob, error) {
	done, err := s.dumpTarget(ctx, target, job)
	if err != nil {
		done = job
		done.Status = domain.BackupStatusFailed
		done.CompletedAt = time.Now().UTC()
		done.Error = err.Error()
	}
	if s.metrics != nil {
		s.metrics.RecordBackupRun(ctx, domain.BackupRunRecord{
			BackupTargetRef: databaseTargetRef(target),
			Succeeded:       err == nil,
			Duration:        time.Since(job.StartedAt),
			SizeBytes:       done.SizeBytes,
			StartedAt:       job.StartedAt,
		})
	}
	return done, err
}

// dumpTarget dumps one database inside its container and uploads the
// dump. pg_dump and the transfer each get their own deadline: a detached
// manual run has no caller left to cancel them.
func (s *Service) dumpTarget(ctx context.Context, target domain.DatabaseTarget, job domain.BackupJob) (domain.BackupJob, error) {
	execCtx, cancelExec := context.WithTimeout(ctx, backupExecTimeout)
	defer cancelExec()
	dumpPath := fmt.Sprintf("/tmp/gordon-backup-%d.bak", job.StartedAt.UnixNano())
	defer s.cleanupDumpFile(target.ContainerID, dumpPath)

	execResult, err := s.runtime.ExecInContainer(execCtx, target.ContainerID, []string{"sh", "-c", pgDumpToPathCommand(dumpPath, target.Database)})
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return job, fmt.Errorf("pg_dump timed out after %s: %w", backupExecTimeout, err)
		}
		return job, err
	}
	if execResult.ExitCode != 0 {
		return job, fmt.Errorf("pg_dump failed with exit code %d: %s", execResult.ExitCode, string(execResult.Stderr))
	}

	// The copy stream is read by the upload, so both share one deadline.
	transferCtx, cancelTransfer := context.WithTimeout(ctx, backupExecTimeout)
	defer cancelTransfer()
	dumpStream, err := s.runtime.CopyFromContainer(transferCtx, target.ContainerID, dumpPath)
	if err != nil {
		if errors.Is(transferCtx.Err(), context.DeadlineExceeded) {
			return job, fmt.Errorf("backup copy timed out after %s: %w", backupExecTimeout, err)
		}
		return job, err
	}
	defer dumpStream.Close()

	counter := &byteCounter{}
	path, err := s.storage.Store(transferCtx, target.App, target.Service, target.Database, job.Schedule, job.StartedAt, io.TeeReader(dumpStream, counter))
	if err != nil {
		if errors.Is(transferCtx.Err(), context.DeadlineExceeded) {
			return job, fmt.Errorf("backup upload timed out after %s: %w", backupExecTimeout, err)
		}
		return job, err
	}

	job.Status = domain.BackupStatusCompleted
	job.CompletedAt = time.Now().UTC()
	job.SizeBytes = counter.n
	job.FilePath = path
	return job, nil
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
