package cli

import (
	"context"
	"testing"
	"time"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestLocalControlPlane_GetStatus(t *testing.T) {
	t.Parallel()

	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)

	ctx := context.Background()
	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{Domain: "app.local", Image: "repo/app:latest"}})
	configSvc.EXPECT().GetRegistryDomain().Return("registry.local")
	configSvc.EXPECT().GetRegistryPort().Return(5000)
	configSvc.EXPECT().GetServerPort().Return(80)
	configSvc.EXPECT().IsAutoRouteEnabled().Return(true)
	configSvc.EXPECT().IsNetworkIsolationEnabled().Return(true)
	containerSvc.EXPECT().List(mock.Anything).Return(map[string]*domain.Container{
		"app.local": {ID: "abc123", Status: "running"},
	})

	cp := &localControlPlane{configSvc: configSvc, containerSvc: containerSvc}
	status, err := cp.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, status.Routes)
	require.Equal(t, "registry.local", status.RegistryDomain)
	require.Equal(t, "running", status.ContainerStatus["app.local"])
}

func TestLocalControlPlane_Backups(t *testing.T) {
	t.Parallel()

	backupSvc := inmocks.NewMockBackupService(t)
	ctx := context.Background()

	now := time.Now().UTC()
	jobs := []domain.BackupJob{{ID: "b1", Domain: "app.local", DBName: "postgres", Status: domain.BackupStatusCompleted, StartedAt: now}}
	backupSvc.EXPECT().ListBackups(mock.Anything, "app.local").Return(jobs, nil)
	backupSvc.EXPECT().Status(mock.Anything).Return(jobs, nil)
	backupSvc.EXPECT().RunBackup(mock.Anything, "app.local", "postgres").Return(&domain.BackupResult{Job: jobs[0]}, nil)
	backupSvc.EXPECT().DetectDatabases(mock.Anything, "app.local").Return([]domain.DBInfo{{Type: domain.DBTypePostgreSQL, Name: "postgres", Host: "postgres", Port: 5432}}, nil)

	cp := &localControlPlane{backupSvc: backupSvc}

	list, err := cp.ListBackups(ctx, "app.local")
	require.NoError(t, err)
	require.Len(t, list, 1)

	status, err := cp.BackupStatus(ctx)
	require.NoError(t, err)
	require.Len(t, status, 1)

	run, err := cp.RunBackup(ctx, "app.local", "postgres")
	require.NoError(t, err)
	require.NotNil(t, run.Backup)
	require.Equal(t, "b1", run.Backup.ID)

	dbs, err := cp.DetectDatabases(ctx, "app.local")
	require.NoError(t, err)
	require.Len(t, dbs, 1)
	require.Equal(t, "postgres", dbs[0].Name)
}

func TestLocalControlPlane_ListTags(t *testing.T) {
	t.Parallel()

	registrySvc := inmocks.NewMockRegistryService(t)
	registrySvc.EXPECT().ListTags(mock.Anything, "repo/app").Return([]string{"v1.0.0", "latest"}, nil)

	cp := &localControlPlane{registrySvc: registrySvc}
	tags, err := cp.ListTags(context.Background(), "repo/app")
	require.NoError(t, err)
	require.Equal(t, []string{"v1.0.0", "latest"}, tags)
}

func TestLocalControlPlane_RestartWithAttachments(t *testing.T) {
	t.Parallel()

	containerSvc := inmocks.NewMockContainerService(t)
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil)
	containerSvc.EXPECT().Restart(mock.Anything, "app.local", true).Return(nil)

	cp := &localControlPlane{containerSvc: containerSvc}
	result, err := cp.Restart(context.Background(), "app.local", true)
	require.NoError(t, err)
	require.Equal(t, "app.local", result.Domain)
}

func TestLocalControlPlane_GetContainerLogs(t *testing.T) {
	t.Parallel()

	logSvc := inmocks.NewMockLogService(t)
	logSvc.EXPECT().GetContainerLogs(mock.Anything, "app.local", 50).Return([]string{"line1", "line2"}, nil)

	cp := &localControlPlane{logSvc: logSvc}
	lines, err := cp.GetContainerLogs(context.Background(), "app.local", 50)
	require.NoError(t, err)
	require.Equal(t, []string{"line1", "line2"}, lines)
}
