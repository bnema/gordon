package cli

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

type localControlPlane struct {
	configSvc       in.ConfigService
	secretSvc       in.SecretService
	containerSvc    in.ContainerService
	backupSvc       in.BackupService
	volumeBackupSvc in.VolumeBackupService
	registrySvc     in.RegistryService
	healthSvc       in.HealthService
	logSvc          in.LogService
	volumeSvc       in.VolumeService
	publicTLSSvc    in.PublicTLSService
	appSvc          in.AppService
}

func NewLocalControlPlane(kernel *app.Kernel) ControlPlane {
	if kernel == nil {
		return &localControlPlane{}
	}

	registrySvc := kernel.Registry()

	return &localControlPlane{
		configSvc:       kernel.Config(),
		secretSvc:       kernel.Secrets(),
		containerSvc:    kernel.Container(),
		backupSvc:       kernel.Backup(),
		volumeBackupSvc: kernel.VolumeBackup(),
		registrySvc:     registrySvc,
		healthSvc:       kernel.Health(),
		logSvc:          kernel.Logs(),
		volumeSvc:       kernel.Volumes(),
		publicTLSSvc:    kernel.PublicTLS(),
		appSvc:          kernel.Apps(),
	}
}

func (l *localControlPlane) ListSecrets(ctx context.Context, secretDomain string) (*remote.SecretsListResult, error) {
	if l.secretSvc == nil {
		return nil, fmt.Errorf("local secret service unavailable")
	}

	keys, err := l.secretSvc.ListKeys(ctx, secretDomain)
	if err != nil {
		return nil, fmt.Errorf("list secret keys: %w", err)
	}

	return &remote.SecretsListResult{
		Domain: secretDomain,
		Keys:   keys,
	}, nil
}

func (l *localControlPlane) SetSecrets(ctx context.Context, secretDomain string, secrets map[string]string) error {
	if l.secretSvc == nil {
		return fmt.Errorf("local secret service unavailable")
	}
	return l.secretSvc.Set(ctx, secretDomain, secrets)
}

func (l *localControlPlane) DeleteSecret(ctx context.Context, secretDomain, key string) error {
	if l.secretSvc == nil {
		return fmt.Errorf("local secret service unavailable")
	}
	return l.secretSvc.Delete(ctx, secretDomain, key)
}

func (l *localControlPlane) GetTLSStatus(ctx context.Context) (*dto.TLSStatusResponse, error) {
	if l.publicTLSSvc == nil {
		return &dto.TLSStatusResponse{
			ACMEEnabled:     false,
			SelectionReason: "public TLS service not configured",
		}, nil
	}

	status := l.publicTLSSvc.Status(ctx)
	result := dto.TLSStatusFromDomain(status)
	return &result, nil
}

func (l *localControlPlane) GetTrafficStatus(_ context.Context) (*dto.TrafficStatusResponse, error) {
	return nil, fmt.Errorf("local traffic status is unavailable from the in-process CLI control plane; query the running Gordon daemon with --remote or set GORDON_REMOTE to its admin URL: %w", domain.ErrTrafficStatusUnavailable)
}

func (l *localControlPlane) GetStatus(ctx context.Context) (*remote.Status, error) {
	if l.configSvc == nil {
		return nil, fmt.Errorf("local config service unavailable")
	}

	status := &remote.Status{
		Apps:             0,
		RegistryDomain:   l.configSvc.GetRegistryDomain(),
		RegistryPort:     l.configSvc.GetRegistryPort(),
		ServerPort:       l.configSvc.GetServerPort(),
		NetworkIsolation: l.configSvc.IsNetworkIsolationEnabled(),
		ContainerStatus:  map[string]string{},
	}

	// App fleet summary from desired/active state (no container inspection).
	if l.appSvc != nil {
		if apps, err := l.appSvc.List(ctx); err == nil {
			status.Apps = len(apps)
			for _, app := range apps {
				status.ContainerStatus[app.App] = localAppStatusLabel(app)
			}
		}
	}

	return status, nil
}

// localAppStatusLabel renders one app's fleet status from its summary.
func localAppStatusLabel(app in.AppSummary) string {
	if app.Stopped {
		return "stopped"
	}
	if app.Active == "" {
		return "pending"
	}
	if app.Converged {
		return "active"
	}
	return "deploying"
}

func (l *localControlPlane) Reload(_ context.Context) error {
	return app.SendReloadSignal()
}

func (l *localControlPlane) ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error) {
	if l.containerSvc == nil {
		return nil, fmt.Errorf("container service unavailable")
	}
	return l.containerSvc.ListNetworks(ctx)
}

func (l *localControlPlane) GetConfig(ctx context.Context) (*remote.Config, error) {
	if l.configSvc == nil {
		return nil, fmt.Errorf("config service unavailable")
	}
	externalRoutes := l.configSvc.GetExternalRoutes()
	externalResponses := make([]remote.ExternalRoute, 0, len(externalRoutes))
	for domainName := range externalRoutes {
		externalResponses = append(externalResponses, remote.ExternalRoute{Domain: domainName})
	}
	sort.Slice(externalResponses, func(i, j int) bool {
		return externalResponses[i].Domain < externalResponses[j].Domain
	})
	cfg := &remote.Config{
		ExternalRoutes: externalResponses,
	}
	cfg.Server.Port = l.configSvc.GetServerPort()
	cfg.Server.RegistryPort = l.configSvc.GetRegistryPort()
	cfg.Server.RegistryDomain = l.configSvc.GetRegistryDomain()
	cfg.NetworkIsolation.Enabled = l.configSvc.IsNetworkIsolationEnabled()
	cfg.NetworkIsolation.Prefix = l.configSvc.GetNetworkPrefix()
	if volumeCfg, ok := any(l.configSvc).(interface{ GetVolumeConfig() (bool, string, bool) }); ok {
		cfg.Volumes.AutoCreate, cfg.Volumes.Prefix, cfg.Volumes.Preserve = volumeCfg.GetVolumeConfig()
	}
	return cfg, nil
}

func (l *localControlPlane) ListTags(ctx context.Context, repository string) ([]string, error) {
	if l.registrySvc == nil {
		return nil, fmt.Errorf("local registry service unavailable")
	}
	return l.registrySvc.ListTags(ctx, repository)
}

func (l *localControlPlane) ListBackups(ctx context.Context, backupDomain string) ([]dto.BackupJob, error) {
	if l.backupSvc == nil {
		return nil, fmt.Errorf("local backup service unavailable")
	}
	jobs, err := l.backupSvc.ListBackups(ctx, backupDomain)
	if err != nil {
		return nil, err
	}
	return toDTOBackupJobs(jobs), nil
}

func (l *localControlPlane) BackupStatus(ctx context.Context) ([]dto.BackupJob, error) {
	if l.backupSvc == nil {
		return nil, fmt.Errorf("local backup service unavailable")
	}
	jobs, err := l.backupSvc.Status(ctx)
	if err != nil {
		return nil, err
	}
	return toDTOBackupJobs(jobs), nil
}

func (l *localControlPlane) RunBackup(ctx context.Context, backupDomain, dbName string) (*dto.BackupRunResponse, error) {
	if l.backupSvc == nil {
		return nil, fmt.Errorf("local backup service unavailable")
	}
	result, err := l.backupSvc.RunBackup(ctx, backupDomain, dbName)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return &dto.BackupRunResponse{Status: "ok"}, nil
	}
	job := toDTOBackupJob(result.Job)
	return &dto.BackupRunResponse{Status: "ok", Backup: &job}, nil
}

func (l *localControlPlane) DetectDatabases(ctx context.Context, backupDomain string) ([]dto.DatabaseInfo, error) {
	if l.backupSvc == nil {
		return nil, fmt.Errorf("local backup service unavailable")
	}
	dbs, err := l.backupSvc.DetectDatabases(ctx, backupDomain)
	if err != nil {
		return nil, err
	}
	out := make([]dto.DatabaseInfo, 0, len(dbs))
	for _, db := range dbs {
		out = append(out, dto.DatabaseInfo{
			Type:        string(db.Type),
			Name:        db.Name,
			Version:     db.Version,
			Host:        db.Host,
			Port:        db.Port,
			ContainerID: db.ContainerID,
			ImageName:   db.ImageName,
		})
	}
	return out, nil
}

func (l *localControlPlane) ListVolumeBackups(ctx context.Context, backupDomain string) ([]dto.VolumeBackupJob, error) {
	if l.volumeBackupSvc == nil {
		return nil, localVolumeBackupServiceUnavailable()
	}
	jobs, err := l.volumeBackupSvc.ListVolumeBackups(ctx, backupDomain)
	if err != nil {
		return nil, fmt.Errorf("list volume backups: %w", err)
	}
	return toDTOVolumeBackupJobs(jobs), nil
}

func (l *localControlPlane) VolumeBackupStatus(ctx context.Context) ([]dto.VolumeBackupJob, error) {
	if l.volumeBackupSvc == nil {
		return nil, localVolumeBackupServiceUnavailable()
	}
	jobs, err := l.volumeBackupSvc.VolumeBackupStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("get volume backup status: %w", err)
	}
	return toDTOVolumeBackupJobs(jobs), nil
}

func (l *localControlPlane) RunVolumeBackups(ctx context.Context, backupDomain, volumeName string) (*dto.VolumeBackupRunResponse, error) {
	if l.volumeBackupSvc == nil {
		return nil, localVolumeBackupServiceUnavailable()
	}
	jobs, err := l.volumeBackupSvc.RunVolumeBackups(ctx, backupDomain, volumeName)
	if err != nil {
		if len(jobs) > 0 {
			resp := &dto.VolumeBackupRunResponse{Status: "partial", Backups: toDTOVolumeBackupJobs(jobs), Error: err.Error()}
			return resp, fmt.Errorf("run volume backups: %w", err)
		}
		return nil, fmt.Errorf("run volume backups: %w", err)
	}
	return &dto.VolumeBackupRunResponse{Status: "ok", Backups: toDTOVolumeBackupJobs(jobs)}, nil
}

func localVolumeBackupServiceUnavailable() error {
	return fmt.Errorf("local volume backup service unavailable: %w", domain.ErrVolumeBackupUnavailable)
}

func (l *localControlPlane) GetProcessLogs(ctx context.Context, lines int) ([]string, error) {
	if l.logSvc == nil {
		return nil, fmt.Errorf("local log service unavailable")
	}
	return l.logSvc.GetProcessLogs(ctx, lines)
}

func (l *localControlPlane) GetContainerLogs(ctx context.Context, logDomain string, lines int) ([]string, error) {
	if l.logSvc == nil {
		return nil, fmt.Errorf("local log service unavailable")
	}
	return l.logSvc.GetContainerLogs(ctx, logDomain, lines)
}

func (l *localControlPlane) StreamProcessLogs(ctx context.Context, lines int) (<-chan string, error) {
	if l.logSvc == nil {
		return nil, fmt.Errorf("local log service unavailable")
	}
	return l.logSvc.FollowProcessLogs(ctx, lines)
}

func (l *localControlPlane) StreamContainerLogs(ctx context.Context, logDomain string, lines int) (<-chan string, error) {
	if l.logSvc == nil {
		return nil, fmt.Errorf("local log service unavailable")
	}
	return l.logSvc.FollowContainerLogs(ctx, logDomain, lines)
}

func (l *localControlPlane) ListVolumes(ctx context.Context) ([]dto.Volume, error) {
	if l.volumeSvc == nil {
		return nil, fmt.Errorf("volume service unavailable")
	}
	vols, err := l.volumeSvc.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]dto.Volume, len(vols))
	for i, v := range vols {
		result[i] = dto.Volume{
			Name:       v.Name,
			Driver:     v.Driver,
			MountPoint: v.MountPoint,
			Size:       v.Size,
			CreatedAt:  v.CreatedAt,
			InUse:      v.InUse,
			Containers: v.Containers,
			Labels:     v.Labels,
		}
	}
	return result, nil
}

func (l *localControlPlane) PruneVolumes(ctx context.Context, req dto.VolumePruneRequest) (*dto.VolumePruneResponse, error) {
	if l.volumeSvc == nil {
		return nil, fmt.Errorf("volume service unavailable")
	}
	report, removed, err := l.volumeSvc.PruneVolumes(ctx, req.DryRun)
	if err != nil {
		return nil, err
	}

	vols := make([]dto.Volume, len(removed))
	for i, v := range removed {
		vols[i] = dto.Volume{
			Name: v.Name,
			Size: v.Size,
		}
	}

	return &dto.VolumePruneResponse{
		VolumesRemoved: report.VolumesRemoved,
		SpaceReclaimed: report.SpaceReclaimed,
		Volumes:        vols,
		Plan:           dto.PruneSummaryFromDomain(report.Plan),
	}, nil
}

func toDTOBackupJobs(jobs []domain.BackupJob) []dto.BackupJob {
	out := make([]dto.BackupJob, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toDTOBackupJob(job))
	}
	return out
}

func toDTOBackupJob(job domain.BackupJob) dto.BackupJob {
	var startedAt *time.Time
	if !job.StartedAt.IsZero() {
		t := job.StartedAt
		startedAt = &t
	}
	var completedAt *time.Time
	if !job.CompletedAt.IsZero() {
		t := job.CompletedAt
		completedAt = &t
	}

	return dto.BackupJob{
		ID:          job.ID,
		Domain:      job.Domain,
		DBName:      job.DBName,
		Schedule:    string(job.Schedule),
		Type:        string(job.Type),
		Status:      string(job.Status),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		SizeBytes:   job.SizeBytes,
		Error:       job.Error,
	}
}

func toDTOVolumeBackupJobs(jobs []domain.VolumeBackupJob) []dto.VolumeBackupJob {
	out := make([]dto.VolumeBackupJob, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toDTOVolumeBackupJob(job))
	}
	return out
}

func toDTOVolumeBackupJob(job domain.VolumeBackupJob) dto.VolumeBackupJob {
	var startedAt *time.Time
	if !job.StartedAt.IsZero() {
		t := job.StartedAt
		startedAt = &t
	}
	var completedAt *time.Time
	if !job.CompletedAt.IsZero() {
		t := job.CompletedAt
		completedAt = &t
	}

	return dto.VolumeBackupJob{
		ID:            job.ID,
		Domain:        job.Domain,
		ContainerName: job.ContainerName,
		ContainerID:   job.ContainerID,
		VolumeName:    job.VolumeName,
		MountPath:     job.MountPath,
		Compression:   job.Metadata["compression"],
		Type:          string(job.Type),
		Status:        string(job.Status),
		StartedAt:     startedAt,
		CompletedAt:   completedAt,
		SizeBytes:     job.SizeBytes,
		ArtifactRef:   job.ArtifactRef,
		Error:         job.Error,
	}
}
