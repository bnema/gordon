# Backup & Cron Services - Implementation Plan

## Overview

Add automated database backup capabilities and a generic cron scheduler to Gordon.
The system auto-detects database attachments, deploys backup sidecar containers,
and manages scheduled backups with retention policies. PostgreSQL 17-18 first,
designed DB-agnostic from day one.

## Architecture Decision: Sidecar Pattern (Approach B)

For each DB attachment with backups enabled, Gordon deploys a **backup sidecar container**
on the same Docker network. The sidecar contains all backup tools and is orchestrated
by Gordon via `ExecInContainer`.

**Why sidecar over exec-into-DB:**
- No dependency on tools being present in the DB image (works with minimal/custom images)
- Sidecar can bundle pgBackRest, pg_dump, pg_basebackup — one image, all tools
- Clean separation: DB container stays untouched, backup logic is isolated
- Same pattern scales to MySQL (xtrabackup sidecar), MongoDB (mongodump sidecar), etc.
- PITR setup doesn't require modifying the running DB container's config at runtime
- Sidecar shares the network → DNS access to the DB (e.g., `postgres:5432`)

**Sidecar lifecycle:**
- Deployed as a special attachment type (`gordon.backup.sidecar=true`)
- Stays running alongside the DB container (long-lived, idle most of the time)
- Gordon triggers backup/restore operations via `ExecInContainer` into the sidecar
- Shares a backup volume for storing dumps and WAL archives

---

## File Structure

```
internal/
├── domain/
│   ├── backup.go                          # BackupJob, BackupResult, DBType, DBInfo, Schedule
│   └── cron.go                            # CronEntry, CronSchedule
│
├── boundaries/
│   ├── in/
│   │   └── backup.go                      # BackupService interface
│   └── out/
│       ├── runtime.go                     # + ExecInContainer, CopyFromContainer (already exists)
│       └── backupstore.go                 # BackupStorage interface
│
├── usecase/
│   ├── cron/
│   │   ├── scheduler.go                   # Generic cron scheduler (Go native, no deps)
│   │   └── scheduler_test.go
│   └── backup/
│       ├── service.go                     # Backup orchestrator
│       ├── service_test.go
│       ├── detector.go                    # DB type auto-detection
│       ├── detector_test.go
│       ├── sidecar.go                     # Sidecar deployment & management
│       ├── sidecar_test.go
│       └── strategy/
│           ├── strategy.go                # BackupStrategy interface
│           ├── postgres.go                # PostgreSQL: pg_dump, pg_basebackup, WAL/pgBackRest
│           └── postgres_test.go
│
├── adapters/
│   ├── in/
│   │   ├── cli/
│   │   │   └── backup.go                  # gordon backup list|run|restore|schedule|status
│   │   └── http/
│   │       └── admin/handler.go           # + backup API endpoints
│   └── out/
│       ├── docker/
│       │   └── runtime.go                 # + ExecInContainer implementation
│       └── filesystem/
│           ├── backup_storage.go          # Backup file storage (local FS)
│           └── backup_storage_test.go
│
├── testutils/
│   └── fixtures/
│       ├── configs/
│       │   └── backup.toml                # Test config with backup settings
│       └── docker/
│           └── docker-compose.test.yml    # Integration test: PG + sidecar
```

---

## 1. Domain Models

### `internal/domain/backup.go`

```go
package domain

import "time"

// DBType identifies the database engine.
type DBType string

const (
    DBTypePostgreSQL DBType = "postgresql"
    DBTypeMySQL      DBType = "mysql"
    DBTypeMariaDB    DBType = "mariadb"
    DBTypeMongoDB    DBType = "mongodb"
    DBTypeUnknown    DBType = "unknown"
)

// DBInfo holds detected database information from an attachment container.
type DBInfo struct {
    Type         DBType
    Version      string            // e.g., "17.2", "18.0"
    Host         string            // DNS hostname in the Docker network
    Port         int               // e.g., 5432
    ContainerID  string            // Docker container ID
    AttachedTo   string            // Domain or group the DB belongs to
    Credentials  map[string]string // Extracted from env vars (user, password, dbname)
    ImageName    string            // Original image reference
}

// BackupSchedule defines when backups run.
type BackupSchedule string

const (
    ScheduleHourly  BackupSchedule = "hourly"
    ScheduleDaily   BackupSchedule = "daily"
    ScheduleWeekly  BackupSchedule = "weekly"
    ScheduleMonthly BackupSchedule = "monthly"
)

// BackupType distinguishes backup methods.
type BackupType string

const (
    BackupTypeLogical  BackupType = "logical"   // pg_dump, mysqldump
    BackupTypePhysical BackupType = "physical"  // pg_basebackup
    BackupTypeWAL      BackupType = "wal"       // WAL archive segment
    BackupTypePITR     BackupType = "pitr"      // Full PITR base backup (pgBackRest)
)

// BackupJob represents a scheduled or manual backup operation.
type BackupJob struct {
    ID           string
    Domain       string         // Which domain's DB
    DBInfo       DBInfo
    Schedule     BackupSchedule // "" for manual
    Type         BackupType
    Status       BackupJobStatus
    StartedAt    time.Time
    CompletedAt  time.Time
    SizeBytes    int64
    FilePath     string         // Path in backup storage
    Error        string         // Error message if failed
    Metadata     map[string]string
}

type BackupJobStatus string

const (
    BackupStatusPending   BackupJobStatus = "pending"
    BackupStatusRunning   BackupJobStatus = "running"
    BackupStatusCompleted BackupJobStatus = "completed"
    BackupStatusFailed    BackupJobStatus = "failed"
)

// BackupResult is returned after a backup operation completes.
type BackupResult struct {
    Job      BackupJob
    Duration time.Duration
}

// RetentionPolicy defines how many backups to keep per schedule tier.
type RetentionPolicy struct {
    Hourly  int // Number of hourly backups to keep
    Daily   int
    Weekly  int
    Monthly int
}

// PITRConfig holds PITR-specific settings.
type PITRConfig struct {
    Enabled       bool
    WALStorageDir string
    Retention     time.Duration // How long to keep WAL archives
}

// BackupConfig is the top-level backup configuration.
type BackupConfig struct {
    Enabled    bool
    StorageDir string
    Retention  RetentionPolicy
    PITR       PITRConfig
    Overrides  map[string]BackupOverride // per domain/attachment override
}

// BackupOverride allows per-attachment backup configuration.
type BackupOverride struct {
    Schedules []BackupSchedule // Which schedules to run
    Retention *RetentionPolicy // Override global retention
    PITR      *bool            // Override global PITR setting
}

// Backup labels for container metadata.
const (
    LabelBackupEnabled  = "gordon.backup"          // "true"/"false"
    LabelBackupType     = "gordon.backup.type"     // "postgresql" — override auto-detect
    LabelBackupVersion  = "gordon.backup.version"  // "17" — override
    LabelBackupSchedule = "gordon.backup.schedule"  // "hourly,daily,weekly"
    LabelBackupSidecar  = "gordon.backup.sidecar"  // "true" on sidecar containers
)
```

### `internal/domain/cron.go`

```go
package domain

import "time"

// CronSchedule represents a recurring schedule.
type CronSchedule struct {
    // Preset schedule (hourly, daily, weekly, monthly)
    // or custom cron expression.
    Preset   BackupSchedule
    CronExpr string // Optional: "0 */6 * * *" style
}

// CronEntry represents a registered cron job.
type CronEntry struct {
    ID        string
    Name      string
    Schedule  CronSchedule
    LastRun   time.Time
    NextRun   time.Time
    Running   bool
}
```

---

## 2. Boundaries (Interfaces)

### `internal/boundaries/out/runtime.go` — additions

```go
// ExecResult holds the result of executing a command in a container.
type ExecResult struct {
    ExitCode int
    Stdout   []byte
    Stderr   []byte
}

// Add to ContainerRuntime interface:

// ExecInContainer executes a command inside a running container.
ExecInContainer(ctx context.Context, containerID string, cmd []string) (*ExecResult, error)
```

Note: `CopyFromContainer` already exists in the Docker adapter but is NOT in the
`ContainerRuntime` interface. It should be promoted to the interface as well since
the backup service needs it.

### `internal/boundaries/out/backupstore.go` — new

```go
type BackupStorage interface {
    // Store saves backup data and returns the storage path.
    Store(ctx context.Context, domain, dbName string, schedule domain.BackupSchedule, timestamp time.Time, data io.Reader) (string, error)

    // Get retrieves a backup by path.
    Get(ctx context.Context, path string) (io.ReadCloser, error)

    // List returns backups for a domain, optionally filtered by schedule.
    List(ctx context.Context, domain string, schedule *domain.BackupSchedule) ([]domain.BackupJob, error)

    // Delete removes a backup.
    Delete(ctx context.Context, path string) error

    // ApplyRetention removes old backups according to the retention policy.
    ApplyRetention(ctx context.Context, domain string, policy domain.RetentionPolicy) (deleted int, err error)
}
```

### `internal/boundaries/in/backup.go` — new

```go
type BackupService interface {
    // ListBackups returns all backups, optionally filtered by domain.
    ListBackups(ctx context.Context, domain string) ([]domain.BackupJob, error)

    // RunBackup triggers an immediate backup for a domain's DB.
    RunBackup(ctx context.Context, domain string, dbName string) (*domain.BackupResult, error)

    // Restore restores a backup by ID.
    Restore(ctx context.Context, domain string, backupID string) error

    // RestorePITR restores to a specific point in time.
    RestorePITR(ctx context.Context, domain string, targetTime time.Time) error

    // Status returns backup health info (last success, failures, next scheduled).
    Status(ctx context.Context) ([]domain.BackupJob, error)

    // DetectDatabases scans attachments and returns detected DBs.
    DetectDatabases(ctx context.Context, domain string) ([]domain.DBInfo, error)
}
```

---

## 3. Cron Scheduler

### `internal/usecase/cron/scheduler.go`

Pure Go scheduler, no external dependencies. Ticks every minute, compares `NextRun` with
`time.Now()`.

```go
type Scheduler struct {
    entries  map[string]*entry
    mu       sync.RWMutex
    stopCh   chan struct{}
    log      zerowrap.Logger
}

type entry struct {
    id       string
    name     string
    schedule Schedule
    job      func(ctx context.Context) error
    lastRun  time.Time
    nextRun  time.Time
    running  atomic.Bool
}

// Key methods:
func NewScheduler(log zerowrap.Logger) *Scheduler
func (s *Scheduler) Add(id, name string, sched Schedule, job func(ctx context.Context) error) error
func (s *Scheduler) Remove(id string)
func (s *Scheduler) Start(ctx context.Context)
func (s *Scheduler) Stop()
func (s *Scheduler) List() []domain.CronEntry
func (s *Scheduler) RunNow(id string) error // Manual trigger
```

**Schedule resolution:**
- `hourly`  → `:00` of every hour
- `daily`   → `02:00` UTC
- `weekly`  → Sunday `03:00` UTC
- `monthly` → 1st of month `04:00` UTC
- Custom cron expression support for future use

---

## 4. DB Auto-Detection

### `internal/usecase/backup/detector.go`

Detection pipeline (cascading, first match wins for type):

```
1. Label override: gordon.backup.type on the container → forced type
2. Image name parsing: "postgres:17-alpine" → PostgreSQL 17
3. Env var inspection: POSTGRES_DB, MYSQL_DATABASE → type confirmation
4. Port inspection: 5432, 3306, 27017 → fallback hint
```

```go
// Known image prefixes → DB type mapping
var imagePatterns = map[string]DBType{
    "postgres":  DBTypePostgreSQL,
    "pgvector":  DBTypePostgreSQL,
    "postgis":   DBTypePostgreSQL,
    "timescale": DBTypePostgreSQL,
    "mysql":     DBTypeMySQL,
    "mariadb":   DBTypeMariaDB,
    "mongo":     DBTypeMongoDB,
    "mongodb":   DBTypeMongoDB,
}

// Known env vars → DB type mapping
var envPatterns = map[string]DBType{
    "POSTGRES_DB":       DBTypePostgreSQL,
    "POSTGRES_USER":     DBTypePostgreSQL,
    "POSTGRES_PASSWORD": DBTypePostgreSQL,
    "PGDATA":            DBTypePostgreSQL,
    "MYSQL_DATABASE":    DBTypeMySQL,
    "MYSQL_ROOT_PASSWORD": DBTypeMySQL,
    "MONGO_INITDB_DATABASE": DBTypeMongoDB,
}

// Known ports → DB type mapping
var portPatterns = map[int]DBType{
    5432:  DBTypePostgreSQL,
    3306:  DBTypeMySQL,
    27017: DBTypeMongoDB,
}

func (d *Detector) DetectAll(ctx context.Context, attachments []domain.Attachment) ([]domain.DBInfo, error)
func (d *Detector) Detect(ctx context.Context, attachment domain.Attachment) (*domain.DBInfo, error)
```

**Credential extraction:**
- Reads env vars from the attachment container via `runtime.InspectImageEnv()`
- Also checks domain secrets (`DomainSecretStore`) for attachment secrets
- Maps to standard keys: `user`, `password`, `database`

---

## 5. Backup Strategy Interface & PostgreSQL Implementation

### `internal/usecase/backup/strategy/strategy.go`

```go
type BackupStrategy interface {
    // Type returns the DB type this strategy handles.
    Type() domain.DBType

    // Dump performs a logical backup (pg_dump, mysqldump, etc.).
    // Executes inside the sidecar container.
    Dump(ctx context.Context, sidecarID string, db domain.DBInfo, opts DumpOptions) (*domain.BackupResult, error)

    // Restore restores from a logical backup.
    Restore(ctx context.Context, sidecarID string, db domain.DBInfo, backupData io.Reader, opts RestoreOptions) error

    // SupportsPITR returns whether this strategy supports point-in-time recovery.
    SupportsPITR() bool

    // SetupPITR configures continuous archiving (WAL archiving, binlog, etc.).
    SetupPITR(ctx context.Context, sidecarID string, db domain.DBInfo, opts PITROptions) error

    // BaseBackup creates a physical base backup for PITR.
    BaseBackup(ctx context.Context, sidecarID string, db domain.DBInfo, opts PITROptions) (*domain.BackupResult, error)

    // RestorePITR restores to a specific point in time.
    RestorePITR(ctx context.Context, sidecarID string, db domain.DBInfo, targetTime time.Time) error

    // SidecarImage returns the Docker image to use for the backup sidecar.
    SidecarImage() string

    // SidecarEnv returns env vars to set on the sidecar container.
    SidecarEnv(db domain.DBInfo) []string

    // SidecarVolumes returns volumes the sidecar needs.
    SidecarVolumes() map[string]string
}
```

### `internal/usecase/backup/strategy/postgres.go`

```go
type PostgresStrategy struct {
    runtime out.ContainerRuntime
    log     zerowrap.Logger
}

func (s *PostgresStrategy) Type() domain.DBType { return domain.DBTypePostgreSQL }
func (s *PostgresStrategy) SupportsPITR() bool   { return true }

// SidecarImage returns a lightweight image with PG client tools + pgBackRest.
// We use the official postgres image matching the DB version — it includes
// pg_dump, pg_restore, pg_basebackup. pgBackRest can be added as a layer.
func (s *PostgresStrategy) SidecarImage() string {
    return "ghcr.io/bnema/gordon-backup-postgres:latest"
    // This image contains: pg_dump, pg_restore, pg_basebackup, pgBackRest
    // Based on: postgres:<version>-alpine + pgbackrest
}
```

**Dump flow:**
```
1. Gordon exec into sidecar: pg_dump -h <db-host> -U <user> -Fc <dbname> > /backups/dump.pgfc
2. Gordon CopyFromContainer(sidecarID, "/backups/dump.pgfc")
3. BackupStorage.Store(data)
4. Clean up temp file in sidecar
```

**PITR flow (pgBackRest in sidecar):**
```
1. Sidecar runs pgBackRest configured to connect to the PG container
2. pgBackRest manages WAL archiving via archive_command (PG → shared volume → sidecar)
3. Scheduled: pgBackRest stanza create + full/incr backups
4. Restore: pgBackRest restore --target=<timestamp> --target-action=promote
```

---

## 6. Sidecar Management

### `internal/usecase/backup/sidecar.go`

```go
type SidecarManager struct {
    runtime  out.ContainerRuntime
    strategy strategy.BackupStrategy
    log      zerowrap.Logger
}

// EnsureSidecar checks if a backup sidecar exists for the given DB, creates one if not.
func (m *SidecarManager) EnsureSidecar(ctx context.Context, db domain.DBInfo, networkName string) (string, error)

// RemoveSidecar removes the backup sidecar for a DB.
func (m *SidecarManager) RemoveSidecar(ctx context.Context, db domain.DBInfo) error

// GetSidecarID returns the container ID of the sidecar for a DB, if it exists.
func (m *SidecarManager) GetSidecarID(ctx context.Context, db domain.DBInfo) (string, error)
```

**Sidecar container spec:**
- Name: `gordon-<domain>-backup-<dbname>` (e.g., `gordon-app-mydomain-com-backup-postgres`)
- Labels: `gordon.managed=true`, `gordon.backup.sidecar=true`, `gordon.attached-to=<domain>`
- Network: same as the DB attachment
- Volumes: backup storage volume + optional WAL archive volume
- Command: `sleep infinity` (idle — Gordon execs commands into it as needed)
- Image: strategy-specific (e.g., `gordon-backup-postgres:latest`)

---

## 7. Backup Service (Orchestrator)

### `internal/usecase/backup/service.go`

```go
type Service struct {
    runtime      out.ContainerRuntime
    storage      out.BackupStorage
    detector     *Detector
    sidecars     *SidecarManager
    strategies   map[domain.DBType]strategy.BackupStrategy
    scheduler    *cron.Scheduler
    config       domain.BackupConfig
    containerSvc in.ContainerService  // To query attachments
    log          zerowrap.Logger
}

func NewService(
    runtime out.ContainerRuntime,
    storage out.BackupStorage,
    containerSvc in.ContainerService,
    scheduler *cron.Scheduler,
    config domain.BackupConfig,
    log zerowrap.Logger,
) *Service

// Start initializes the backup service: detects DBs, deploys sidecars, registers schedules.
func (s *Service) Start(ctx context.Context) error

// Stop cleans up (but does NOT remove sidecars — they persist).
func (s *Service) Stop()

// RunBackup triggers an immediate backup.
func (s *Service) RunBackup(ctx context.Context, domain, dbName string) (*domain.BackupResult, error)

// RunScheduled is called by the cron scheduler.
func (s *Service) RunScheduled(ctx context.Context, schedule domain.BackupSchedule) error

// ListBackups returns stored backups.
func (s *Service) ListBackups(ctx context.Context, domain string) ([]domain.BackupJob, error)

// Restore restores from a specific backup.
func (s *Service) Restore(ctx context.Context, domain, backupID string) error

// RestorePITR restores to a point in time.
func (s *Service) RestorePITR(ctx context.Context, domain string, targetTime time.Time) error

// DetectDatabases scans attachments and returns detected DB info.
func (s *Service) DetectDatabases(ctx context.Context, domain string) ([]domain.DBInfo, error)

// Status returns backup health summary.
func (s *Service) Status(ctx context.Context) ([]domain.BackupJob, error)
```

**Startup flow:**
```
BackupService.Start()
  → For each domain with attachments:
    → detector.DetectAll(attachments) → []DBInfo
    → For each detected DB:
      → strategy = strategies[db.Type]
      → sidecar.EnsureSidecar(db, networkName) → sidecarContainerID
      → If PITR enabled: strategy.SetupPITR(sidecarID, db)
  → Register cron jobs:
    → scheduler.Add("backup-hourly",  hourly,  RunScheduled(hourly))
    → scheduler.Add("backup-daily",   daily,   RunScheduled(daily))
    → scheduler.Add("backup-weekly",  weekly,  RunScheduled(weekly))
    → scheduler.Add("backup-monthly", monthly, RunScheduled(monthly))
    → scheduler.Add("backup-retention", daily, ApplyRetention())
```

---

## 8. Configuration (TOML)

```toml
[backups]
enabled = true
storage_dir = "/var/lib/gordon/backups"

[backups.retention]
hourly = 24      # keep 24 hourly backups
daily = 7        # keep 7 daily backups
weekly = 4       # keep 4 weekly backups
monthly = 12     # keep 12 monthly backups

[backups.pitr]
enabled = false                              # opt-in globally
wal_storage_dir = "/var/lib/gordon/wal-archive"
retention = "7d"                             # keep 7 days of WAL archives

# Per-attachment overrides
[backups.overrides."app.example.com/postgres"]
schedules = ["daily", "weekly"]              # only daily & weekly, no hourly
pitr = true                                  # enable PITR for this one

[backups.overrides."api.example.com/postgres"]
schedules = ["hourly", "daily", "weekly", "monthly"]
retention.daily = 14                         # override: keep 14 daily
```

---

## 9. CLI Commands

### `internal/adapters/in/cli/backup.go`

```bash
# List backups
gordon backup list                                    # all domains
gordon backup list app.example.com                    # specific domain
gordon backup list app.example.com --schedule daily   # filter by schedule

# Run manual backup
gordon backup run app.example.com                     # auto-detect DB, backup now
gordon backup run app.example.com --db postgres       # specific DB if multiple

# Restore
gordon backup restore app.example.com <backup-id>
gordon backup restore app.example.com --pitr "2025-01-15T14:30:00Z"

# Schedule info
gordon backup schedule list                           # show all active schedules
gordon backup schedule list app.example.com           # for a domain

# Health/status
gordon backup status                                  # last success/failure per domain

# DB detection (debug/info)
gordon backup detect                                  # show all detected DBs
gordon backup detect app.example.com                  # for a domain

# Remote mode support
gordon backup list --remote https://gordon.example.com --token $TOKEN
```

---

## 10. Admin API Endpoints

### Additions to `internal/adapters/in/http/admin/handler.go`

```
GET    /admin/backups                           # List all backups
GET    /admin/backups/{domain}                  # List backups for domain
POST   /admin/backups/{domain}                  # Trigger manual backup
POST   /admin/backups/{domain}/restore/{id}     # Restore from backup
POST   /admin/backups/{domain}/pitr             # PITR restore (body: {"target_time": "..."})
GET    /admin/backups/status                    # Backup health summary
GET    /admin/backups/{domain}/detect           # Show detected DBs
GET    /admin/backups/schedules                 # List active schedules
```

---

## 11. Docker Runtime Additions

### `internal/adapters/out/docker/runtime.go`

**New method — `ExecInContainer`:**

```go
func (r *Runtime) ExecInContainer(ctx context.Context, containerID string, cmd []string) (*out.ExecResult, error) {
    // 1. ContainerExecCreate — create the exec instance
    // 2. ContainerExecAttach — attach to get stdout/stderr
    // 3. ContainerExecInspect — get exit code
    // Uses Docker SDK: client.ContainerExecCreate, ContainerExecAttach, ContainerExecInspect
}
```

**Promote to interface — `CopyFromContainer`:**

Already implemented in the adapter (`runtime.go:1110`), but not declared in the
`ContainerRuntime` interface. Add it:

```go
// In boundaries/out/runtime.go:
CopyFromContainer(ctx context.Context, containerID, srcPath string) ([]byte, error)
```

---

## 12. Tests

### Unit Tests (mockery mocks)

#### Mockery config additions (`.mockery.yaml`)

```yaml
# Add to existing packages:
github.com/bnema/gordon/internal/boundaries/out:
  interfaces:
    # ... existing ...
    BackupStorage:        # NEW

github.com/bnema/gordon/internal/boundaries/in:
  interfaces:
    # ... existing ...
    BackupService:        # NEW
```

#### Test files

| File | Tests |
|------|-------|
| `usecase/cron/scheduler_test.go` | Scheduler tick, add/remove jobs, concurrent safety, manual trigger, preset schedules |
| `usecase/backup/detector_test.go` | Image name parsing (postgres:17, pgvector/pgvector:pg17, custom), env var detection, port fallback, label override, unknown images |
| `usecase/backup/service_test.go` | RunBackup flow (mock runtime + storage), RunScheduled, retention cleanup, error handling, PITR setup |
| `usecase/backup/sidecar_test.go` | EnsureSidecar create/reuse, RemoveSidecar, sidecar container config validation |
| `usecase/backup/strategy/postgres_test.go` | Dump command construction, restore command, PITR setup commands, version handling |
| `adapters/out/docker/runtime_exec_test.go` | ExecInContainer (requires Docker or mock client) |
| `adapters/out/filesystem/backup_storage_test.go` | Store, List, Get, Delete, ApplyRetention (uses temp dirs) |

#### Mock usage pattern

```go
// Example: service_test.go
func TestRunBackup_PostgreSQL(t *testing.T) {
    mockRuntime := mocks.NewMockContainerRuntime(t)
    mockStorage := mocks.NewMockBackupStorage(t)
    mockContainerSvc := mocks.NewMockContainerService(t)

    // Setup expectations
    mockContainerSvc.EXPECT().
        ListAttachments(mock.Anything, "app.example.com").
        Return([]domain.Attachment{
            {Name: "postgres", Image: "postgres:17", ContainerID: "abc123", Status: "running"},
        }, nil)

    mockRuntime.EXPECT().
        InspectImageEnv(mock.Anything, "postgres:17").
        Return([]string{"POSTGRES_DB=mydb", "POSTGRES_USER=app", "PGDATA=/var/lib/postgresql/data"}, nil)

    mockRuntime.EXPECT().
        ExecInContainer(mock.Anything, "sidecar-id", mock.MatchedBy(func(cmd []string) bool {
            return cmd[0] == "pg_dump"
        })).
        Return(&out.ExecResult{ExitCode: 0, Stdout: []byte("dump data")}, nil)

    mockStorage.EXPECT().
        Store(mock.Anything, "app.example.com", "postgres", mock.Anything, mock.Anything, mock.Anything).
        Return("/backups/app.example.com/postgres/2025-01-15T10-00-00.pgfc", nil)

    // Create service and run
    svc := backup.NewService(mockRuntime, mockStorage, mockContainerSvc, scheduler, config, log)
    result, err := svc.RunBackup(ctx, "app.example.com", "postgres")

    assert.NoError(t, err)
    assert.Equal(t, domain.BackupStatusCompleted, result.Job.Status)
}
```

### Integration Tests (local dev)

#### `internal/testutils/fixtures/docker/docker-compose.test.yml`

A docker-compose file for spinning up a real PostgreSQL + sidecar locally to test
the full backup/restore flow end-to-end.

```yaml
services:
  postgres:
    image: postgres:17-alpine
    environment:
      POSTGRES_DB: testdb
      POSTGRES_USER: testuser
      POSTGRES_PASSWORD: testpass
    ports:
      - "15432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
    networks:
      - gordon-test

  backup-sidecar:
    image: postgres:17-alpine  # or gordon-backup-postgres:latest once built
    command: ["sleep", "infinity"]
    environment:
      PGHOST: postgres
      PGUSER: testuser
      PGPASSWORD: testpass
      PGDATABASE: testdb
    volumes:
      - backups:/backups
    depends_on:
      - postgres
    networks:
      - gordon-test

volumes:
  pgdata:
  backups:

networks:
  gordon-test:
    driver: bridge
```

#### `internal/usecase/backup/integration_test.go`

```go
//go:build integration

package backup_test

// Tests tagged with `integration` build tag.
// Run with: go test -tags=integration -v ./internal/usecase/backup/
//
// Prerequisites:
//   docker compose -f internal/testutils/fixtures/docker/docker-compose.test.yml up -d
//
// Test cases:
// - TestIntegration_DetectPostgres: detect PG from running container
// - TestIntegration_DumpAndRestore: full pg_dump → restore cycle
// - TestIntegration_PITRSetup: configure WAL archiving, verify segments
// - TestIntegration_RetentionCleanup: create N backups, verify retention
// - TestIntegration_SidecarLifecycle: deploy sidecar, exec, remove
```

#### Makefile additions

```makefile
# Integration test targets
test-integration-backup:
	docker compose -f internal/testutils/fixtures/docker/docker-compose.test.yml up -d --wait
	go test -tags=integration -v -count=1 ./internal/usecase/backup/...
	docker compose -f internal/testutils/fixtures/docker/docker-compose.test.yml down -v

test-integration-backup-keep:
	docker compose -f internal/testutils/fixtures/docker/docker-compose.test.yml up -d --wait
	go test -tags=integration -v -count=1 ./internal/usecase/backup/... || true
	@echo "Containers still running — use 'make test-integration-backup-down' to clean up"

test-integration-backup-down:
	docker compose -f internal/testutils/fixtures/docker/docker-compose.test.yml down -v
```

---

## 13. Documentation Updates

### New docs

| File | Content |
|------|---------|
| `docs/config/backups.md` | Full backup configuration reference (TOML options, retention, PITR, overrides) |
| `docs/cli/backup.md` | CLI command reference (list, run, restore, schedule, status, detect) |

### Updated docs

| File | Changes |
|------|---------|
| `docs/config/index.md` | Add "Backups" to config section list |
| `docs/config/attachments.md` | Add "Automatic Backups" section — link to backups.md, mention auto-detection |
| `docs/cli/index.md` | Add `gordon backup` to CLI commands list |
| `docs/reference/docker-labels.md` | Add backup labels table (`gordon.backup`, `gordon.backup.type`, etc.) |
| `docs/concepts.md` | Add "Backups & Recovery" to concepts overview |
| `docs/reference/index.md` | Add link to backup-related reference |

---

## 14. Dependency Injection Wiring

### `internal/app/run.go` additions

```go
// In createServices():
func createBackupService(ctx context.Context, v *viper.Viper, svc *services, log zerowrap.Logger) (*backup.Service, error) {
    // 1. Parse backup config from viper
    // 2. Create BackupStorage (filesystem adapter)
    // 3. Create Scheduler
    // 4. Create Detector
    // 5. Register strategies (PostgresStrategy for now)
    // 6. Create BackupService
    // 7. If backups enabled: call backupSvc.Start()
    return svc, nil
}

// Register in services struct:
type services struct {
    // ... existing fields ...
    backupSvc    *backup.Service
    scheduler    *cron.Scheduler
}
```

---

## 15. Events

### New event types in `internal/domain/event.go`

```go
const (
    EventBackupStarted   EventType = "backup.started"
    EventBackupCompleted EventType = "backup.completed"
    EventBackupFailed    EventType = "backup.failed"
    EventRestoreStarted  EventType = "restore.started"
    EventRestoreCompleted EventType = "restore.completed"
)
```

These integrate with the existing EventBus so that:
- Config reload re-scans attachments and adjusts backup schedules
- Container deploy events trigger sidecar deployment for new DBs
- Backup events can be consumed by future notification systems (webhook, etc.)

---

## 16. Implementation Order

| Phase | Task | Depends On |
|-------|------|------------|
| **1** | `ExecInContainer` in runtime interface + Docker adapter | — |
| **1** | Promote `CopyFromContainer` to interface | — |
| **1** | Regenerate mocks (`make mocks`) | Phase 1 interface changes |
| **2** | Domain models (`backup.go`, `cron.go`) | — |
| **2** | `BackupStorage` interface + filesystem adapter | Domain models |
| **2** | `BackupStorage` adapter tests | Adapter |
| **3** | Cron scheduler + tests | — |
| **4** | DB detector + tests | Domain models, runtime interface |
| **5** | `BackupStrategy` interface + PostgreSQL strategy + tests | Runtime, domain |
| **6** | Sidecar manager + tests | Runtime, strategy |
| **7** | Backup service (orchestrator) + tests | All above |
| **8** | CLI commands (`gordon backup ...`) | Backup service |
| **8** | Admin API endpoints | Backup service |
| **9** | Config TOML integration (`[backups]` section) | Config service |
| **9** | DI wiring in `app/run.go` | All above |
| **10** | `.mockery.yaml` update + regenerate | New interfaces |
| **10** | Integration test infrastructure (docker-compose, Makefile targets) | All above |
| **10** | Integration tests | Infrastructure |
| **11** | Documentation (new + updates) | Feature complete |
| **12** | PITR implementation (pgBackRest in sidecar) | Logical backups working |

---

## 17. Sidecar Docker Image (future)

The backup sidecar needs a dedicated image. For MVP, we can use the official
`postgres:<version>-alpine` image which includes `pg_dump`, `pg_restore`, `pg_basebackup`.

For PITR (Phase 12), we'll need a custom image with pgBackRest:

```dockerfile
# ghcr.io/bnema/gordon-backup-postgres
ARG PG_VERSION=17
FROM postgres:${PG_VERSION}-alpine

RUN apk add --no-cache pgbackrest

# Backup scripts (optional helpers)
COPY scripts/ /usr/local/bin/

CMD ["sleep", "infinity"]
```

Multi-version support: build matrix for PG 17 and 18 tags.

---

## Open Items (for later discussion)

- **Remote storage (S3, etc.)**: The `BackupStorage` interface is ready for it —
  just add a new adapter. Not needed for MVP.
- **Compression**: `pg_dump -Fc` already uses zlib compression. For physical backups,
  consider zstd (configurable).
- **Notifications**: Events are in place (`EventBackupFailed`). Webhook adapter is a
  natural next step.
- **Backup encryption**: Encrypt at rest with a configurable key. Add to BackupStorage
  as a wrapping layer.
- **Concurrent backups**: The scheduler should NOT run the same domain's backup
  concurrently. Use per-domain locks (same pattern as deploy locks).
