package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/adapters/dto"
	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type previewContainerService struct {
	*inmocks.MockContainerService
	preview func(context.Context, string) (*domain.CleanupReport, error)
}

func (s *previewContainerService) PreviewRemovedRouteCleanup(ctx context.Context, routeDomain string) (*domain.CleanupReport, error) {
	if s.preview == nil {
		return nil, nil
	}
	return s.preview(ctx, routeDomain)
}

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

func TestLocalControlPlane_DeployUsesInternalDeployContext(t *testing.T) {
	t.Parallel()

	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)

	ctx := context.Background()
	route := &domain.Route{Domain: "app.local", Image: "repo/app:latest"}

	require.False(t, domain.IsInternalDeploy(ctx))

	configSvc.EXPECT().GetRoute(mock.Anything, "app.local").Return(route, nil)
	containerSvc.EXPECT().Deploy(mock.Anything, *route).RunAndReturn(func(deployCtx context.Context, deployedRoute domain.Route) (*domain.Container, error) {
		require.True(t, domain.IsInternalDeploy(deployCtx))
		require.Equal(t, *route, deployedRoute)
		return &domain.Container{ID: "container-1"}, nil
	})

	cp := &localControlPlane{configSvc: configSvc, containerSvc: containerSvc}
	result, err := cp.Deploy(ctx, "app.local")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "deployed", result.Status)
	require.Equal(t, "app.local", result.Domain)
	require.Equal(t, "container-1", result.ContainerID)
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

func TestLocalControlPlane_RunVolumeBackupsPreservesPartialJobs(t *testing.T) {
	t.Parallel()

	volumeBackupSvc := inmocks.NewMockVolumeBackupService(t)
	ctx := context.Background()
	runErr := errors.New("one volume failed")
	jobs := []domain.VolumeBackupJob{{ID: "v1", Domain: "app.local", VolumeName: "gordon-app-data", Status: domain.BackupStatusCompleted}}
	volumeBackupSvc.EXPECT().RunVolumeBackups(mock.Anything, "app.local", "").Return(jobs, runErr)

	cp := &localControlPlane{volumeBackupSvc: volumeBackupSvc}
	result, err := cp.RunVolumeBackups(ctx, "app.local", "")

	require.ErrorIs(t, err, runErr)
	require.NotNil(t, result)
	assert.Equal(t, "partial", result.Status)
	assert.Equal(t, runErr.Error(), result.Error)
	require.Len(t, result.Backups, 1)
	assert.Equal(t, "v1", result.Backups[0].ID)
}

func TestLocalControlPlane_RunVolumeBackupsMissingServiceUsesSentinel(t *testing.T) {
	t.Parallel()

	cp := &localControlPlane{}
	result, err := cp.RunVolumeBackups(context.Background(), "app.local", "")

	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, domain.ErrVolumeBackupUnavailable)
}

func TestLocalControlPlane_ListVolumeBackupsWrapsServiceError(t *testing.T) {
	t.Parallel()

	volumeBackupSvc := inmocks.NewMockVolumeBackupService(t)
	wantErr := errors.New("store unavailable")
	volumeBackupSvc.EXPECT().ListVolumeBackups(mock.Anything, "app.local").Return(nil, wantErr)

	cp := &localControlPlane{volumeBackupSvc: volumeBackupSvc}
	jobs, err := cp.ListVolumeBackups(context.Background(), "app.local")

	require.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "list volume backups")
	assert.Nil(t, jobs)
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

func TestLocalControlPlane_RemoveRoutePersistsConfigThenReconcilesRuntime(t *testing.T) {
	t.Parallel()

	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	report := &domain.CleanupReport{
		Domain: "app.local",
		RemovedContainers: []domain.CleanupContainer{{
			ID:   "container-1",
			Name: "gordon-app.local",
		}},
	}

	removeCall := configSvc.EXPECT().RemoveRoute(mock.Anything, "app.local").Return(nil).Once()
	cleanupCall := containerSvc.EXPECT().ReconcileRemovedRoute(mock.Anything, "app.local").Return(report, nil).Once()
	mock.InOrder(removeCall, cleanupCall)

	cp := &localControlPlane{configSvc: configSvc, containerSvc: containerSvc}
	err := cp.RemoveRoute(context.Background(), "app.local")
	require.NoError(t, err)
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

func TestLocalControlPlane_ListRoutesWithDetailsSyncsBeforeListing(t *testing.T) {
	t.Parallel()

	containerSvc := inmocks.NewMockContainerService(t)
	syncCall := containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil).Once()
	listCall := containerSvc.EXPECT().ListRoutesWithDetails(mock.Anything).Return([]domain.RouteInfo{{
		Domain:          "app.local",
		Image:           "repo/app:latest",
		ContainerID:     "container-1",
		ContainerStatus: "running",
	}}).Once()
	mock.InOrder(syncCall, listCall)

	cp := &localControlPlane{containerSvc: containerSvc}
	routes, err := cp.ListRoutesWithDetails(context.Background())
	require.NoError(t, err)
	require.Len(t, routes, 1)
	require.Equal(t, "app.local", routes[0].Domain)
	require.Equal(t, "repo/app:latest", routes[0].Image)
	require.Equal(t, "container-1", routes[0].ContainerID)
	require.Equal(t, "running", routes[0].ContainerStatus)
}

func TestLocalControlPlane_ListRoutesWithDetails_IncludesConfiguredRouteWithoutRuntimeDetail(t *testing.T) {
	t.Parallel()

	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)

	configSvc.EXPECT().GetRoutes(mock.Anything).Return([]domain.Route{{
		Domain: "app.local",
		Image:  "repo/app:latest",
	}}).Once()
	containerSvc.EXPECT().SyncContainers(mock.Anything).Return(nil).Once()
	containerSvc.EXPECT().ListRoutesWithDetails(mock.Anything).Return(nil).Once()

	cp := &localControlPlane{configSvc: configSvc, containerSvc: containerSvc}
	routes, err := cp.ListRoutesWithDetails(context.Background())
	require.NoError(t, err)
	require.Len(t, routes, 1)
	require.Equal(t, "app.local", routes[0].Domain)
	require.Equal(t, "repo/app:latest", routes[0].Image)
	require.Empty(t, routes[0].ContainerID)
	require.Empty(t, routes[0].ContainerStatus)
}

func TestLocalControlPlane_GetTLSStatusWithoutService(t *testing.T) {
	t.Parallel()

	cp := &localControlPlane{}
	status, err := cp.GetTLSStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, &dto.TLSStatusResponse{
		ACMEEnabled:     false,
		SelectionReason: "public TLS service not configured",
	}, status)
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

func TestLocalControlPlane_GetRouteCleanupPreview_ReturnsNilReportWithoutPanic(t *testing.T) {
	ctx := context.Background()
	volumeSvc := inmocks.NewMockVolumeService(t)
	containerSvc := &previewContainerService{
		MockContainerService: inmocks.NewMockContainerService(t),
		preview: func(context.Context, string) (*domain.CleanupReport, error) {
			return nil, nil
		},
	}
	cp := &localControlPlane{containerSvc: containerSvc, volumeSvc: volumeSvc}

	report, err := cp.GetRouteCleanupPreview(ctx, "app.local")
	require.NoError(t, err)
	assert.Nil(t, report)
}

func TestLocalControlPlane_RemoveRouteReconcilesRuntimeWhenRouteAlreadyMissing(t *testing.T) {
	ctx := context.Background()
	configSvc := inmocks.NewMockConfigService(t)
	containerSvc := inmocks.NewMockContainerService(t)
	report := &domain.CleanupReport{Domain: "app.local"}

	configSvc.EXPECT().RemoveRoute(mock.Anything, "app.local").Return(domain.ErrRouteNotFound).Once()
	containerSvc.EXPECT().ReconcileRemovedRoute(mock.Anything, "app.local").Return(report, nil).Once()

	cp := &localControlPlane{configSvc: configSvc, containerSvc: containerSvc}
	resp, err := cp.RemoveRouteWithCleanup(ctx, "app.local")
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, dto.CleanupReportFromDomain(report), resp.Cleanup)
}
