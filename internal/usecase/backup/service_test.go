package backup

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// appSpecWithDatabase builds one service that declares a database and
// references it from its backup declaration.
func appSpecWithDatabase(service, database, schedule string) domain.AppService {
	return domain.AppService{
		Name:      service,
		Image:     "postgres:17",
		Databases: []domain.AppDatabase{{Name: database, Type: domain.AppDBPostgres, Schedule: schedule}},
		Backup:    domain.AppBackup{Postgres: []string{database}},
	}
}

// activeWith builds an ACTIVE record holding one effective service.
func activeWith(app string, services map[string]domain.AppService, container string) domain.AppActive {
	effective := make(map[string]domain.AppEffectiveService, len(services))
	for name, spec := range services {
		effective[name] = domain.AppEffectiveService{
			EffectiveRevision: "rev-1",
			Container:         container,
			Spec:              spec,
		}
	}
	return domain.AppActive{App: app, Converged: true, ConvergedRevision: "rev-1", Services: effective}
}

func backupTestService(t *testing.T, state *outmocks.MockAppStateReader) (*outmocks.MockContainerRuntime, *outmocks.MockBackupStorage, *Service) {
	t.Helper()
	runtime := outmocks.NewMockContainerRuntime(t)
	storage := outmocks.NewMockBackupStorage(t)
	svc := &Service{runtime: runtime, storage: storage, log: zerowrap.Default(), state: state}
	return runtime, storage, svc
}

// TestService_TargetsResolveFromDeclarations proves a target exists only
// because the ACTIVE record declares it: no image, port, or label is
// inspected.
func TestService_TargetsResolveFromDeclarations(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": appSpecWithDatabase("api", "orders", "daily"),
		"web": appSpecWithDatabase("web", "users", "hourly"),
	}, "ctr-api"), true, nil).Once()

	_, _, svc := backupTestService(t, state)
	targets, err := svc.Targets(ctx, "shop")
	require.NoError(t, err)
	require.Len(t, targets, 2)
	assert.Equal(t, domain.DatabaseTarget{
		App: "shop", Service: "api", Database: "orders",
		Schedule: domain.ScheduleDaily, ContainerID: "ctr-api",
	}, targets[0])
	assert.Equal(t, "users", targets[1].Database)
	assert.Equal(t, domain.ScheduleHourly, targets[1].Schedule)
}

// TestService_TargetsIgnoreUnreferencedDatabases proves a declared
// database the backup declaration does not reference is not a target.
func TestService_TargetsIgnoreUnreferencedDatabases(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	spec := appSpecWithDatabase("api", "orders", "daily")
	spec.Databases = append(spec.Databases, domain.AppDatabase{Name: "metrics", Type: domain.AppDBPostgres, Schedule: "daily"})
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{"api": spec}, "ctr-api"), true, nil).Once()

	_, _, svc := backupTestService(t, state)
	targets, err := svc.Targets(ctx, "shop")
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "orders", targets[0].Database)
}

// TestService_TargetsSkipStoppedApps proves a stopped app has no target:
// its workloads are intentionally down.
func TestService_TargetsSkipStoppedApps(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").
		Return(domain.AppStopIntent{App: "shop", Stopped: true}, nil).Once()

	_, _, svc := backupTestService(t, state)
	targets, err := svc.Targets(ctx, "shop")
	require.NoError(t, err)
	assert.Empty(t, targets)
}

// TestService_RunBackup_StoresUnderAppIdentity proves a run dumps from the
// declared service container and stores the artifact under the app name.
func TestService_RunBackup_StoresUnderAppIdentity(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": appSpecWithDatabase("api", "orders", "daily"),
	}, "ctr-api"), true, nil).Once()

	runtime, storage, svc := backupTestService(t, state)
	runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.MatchedBy(func(cmd []string) bool {
		return len(cmd) == 3 && strings.Contains(cmd[2], "'orders'") && !strings.Contains(cmd[2], "POSTGRES_DB")
	})).Return(&out.ExecResult{ExitCode: 0}, nil).Once()
	runtime.EXPECT().CopyFromContainer(mock.Anything, "ctr-api", mock.Anything).
		Return(io.NopCloser(strings.NewReader("dump-bytes")), nil).Once()
	runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.MatchedBy(func(cmd []string) bool {
		return len(cmd) == 3 && strings.HasPrefix(cmd[2], "rm -f ")
	})).Return(&out.ExecResult{ExitCode: 0}, nil).Once()
	storage.EXPECT().Store(mock.Anything, "shop", "api", "orders", domain.BackupSchedule(""), mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, _ domain.BackupSchedule, _ time.Time, data io.Reader) (string, error) {
			if _, err := io.Copy(io.Discard, data); err != nil {
				return "", err
			}
			return "/backups/shop/orders/manual/file.bak", nil
		}).Once()

	result, err := svc.RunBackup(ctx, "shop", "api", "orders")
	require.NoError(t, err)
	assert.Equal(t, "shop", result.Job.App)
	assert.Equal(t, "api", result.Job.Service)
	assert.Equal(t, "orders", result.Job.DBName)
	assert.Equal(t, int64(len("dump-bytes")), result.Job.SizeBytes)
	assert.Equal(t, domain.BackupStatusCompleted, result.Job.Status)
}

// TestService_RunBackup_SelectorRules proves the explicit-selector rules:
// a missing selector with several candidates lists them, and an unknown
// target is not found.
func TestService_RunBackup_SelectorRules(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	active := activeWith("shop", map[string]domain.AppService{
		"api": appSpecWithDatabase("api", "orders", "daily"),
		"web": appSpecWithDatabase("web", "users", "daily"),
	}, "ctr-x")
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(active, true, nil)

	_, _, svc := backupTestService(t, state)

	_, err := svc.RunBackup(ctx, "shop", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "api/orders")
	assert.Contains(t, err.Error(), "web/users")

	_, err = svc.RunBackup(ctx, "shop", "api", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = svc.RunBackup(ctx, "shop", "missing", "orders")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestService_RunForSchedule_RunsOnlyTheDeclaredSchedule proves the
// scheduler filters by each database's own declaration.
func TestService_RunForSchedule_RunsOnlyTheDeclaredSchedule(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"shop"}, nil).Once()
	spec := appSpecWithDatabase("api", "orders", "daily")
	spec.Databases = append(spec.Databases, domain.AppDatabase{Name: "users", Type: domain.AppDBPostgres, Schedule: "hourly"})
	spec.Backup.Postgres = append(spec.Backup.Postgres, "users")
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{"api": spec}, "ctr-api"), true, nil).Once()

	runtime, storage, svc := backupTestService(t, state)
	runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.MatchedBy(func(cmd []string) bool {
		return strings.Contains(cmd[2], "'users'")
	})).Return(&out.ExecResult{ExitCode: 0}, nil).Once()
	runtime.EXPECT().CopyFromContainer(mock.Anything, "ctr-api", mock.Anything).
		Return(io.NopCloser(strings.NewReader("x")), nil).Once()
	runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.MatchedBy(func(cmd []string) bool {
		return strings.HasPrefix(cmd[2], "rm -f ")
	})).Return(&out.ExecResult{ExitCode: 0}, nil).Once()
	storage.EXPECT().Store(mock.Anything, "shop", "api", "users", domain.ScheduleHourly, mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, _ domain.BackupSchedule, _ time.Time, data io.Reader) (string, error) {
			if _, err := io.Copy(io.Discard, data); err != nil {
				return "", err
			}
			return "/backups/shop/users/hourly/file.bak", nil
		}).Once()
	storage.EXPECT().ApplyRetention(mock.Anything, "shop", mock.Anything).Return(0, nil).Once()

	require.NoError(t, svc.RunForSchedule(ctx, domain.ScheduleHourly))
}

// TestService_RunForSchedule_RejectsUnknownSchedule proves a caller cannot
// invent a schedule tier.
func TestService_RunForSchedule_RejectsUnknownSchedule(t *testing.T) {
	state := outmocks.NewMockAppStateReader(t)
	_, _, svc := backupTestService(t, state)
	require.Error(t, svc.RunForSchedule(context.Background(), domain.BackupSchedule("sometimes")))
}

// TestService_ListBackupsUsesAppIdentity proves listing is app-keyed and
// never domain-keyed.
func TestService_ListBackupsUsesAppIdentity(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"shop"}, nil).Once()

	_, storage, svc := backupTestService(t, state)
	storage.EXPECT().List(mock.Anything, "shop", mock.Anything).Return([]domain.BackupJob{{
		ID: "job-1", App: "shop", Service: "api", DBName: "orders",
		Status: domain.BackupStatusCompleted, StartedAt: time.Now().UTC(),
	}}, nil).Once()

	jobs, err := svc.ListBackups(ctx, "")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "shop", jobs[0].App)
}
