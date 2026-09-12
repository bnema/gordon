package cli

import (
	"context"
	"fmt"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

// remoteControlPlane delegates every CLI operation to one daemon client:
// the explicit remote or the owner-only local admin socket.
type remoteControlPlane struct {
	client *remote.Client
}

// NewRemoteControlPlane creates the daemon-backed control plane.
func NewRemoteControlPlane(client *remote.Client) ControlPlane {
	return &remoteControlPlane{client: client}
}

func (r *remoteControlPlane) ApplyApp(ctx context.Context, req dto.AppApplyRequest) (*dto.AppApplyResponse, error) {
	return r.client.ApplyApp(ctx, req)
}

func (r *remoteControlPlane) ListApps(ctx context.Context) ([]dto.AppSummaryDTO, error) {
	return r.client.ListApps(ctx)
}

func (r *remoteControlPlane) ShowApp(ctx context.Context, app string) (*dto.AppShowResponse, error) {
	return r.client.ShowApp(ctx, app)
}

func (r *remoteControlPlane) DiffApp(ctx context.Context, app string) (*dto.AppDiffResponse, error) {
	return r.client.DiffApp(ctx, app)
}

func (r *remoteControlPlane) DeployApp(ctx context.Context, app string, req dto.AppDeployRequest) (*dto.AppDeployResponse, string, error) {
	return r.client.DeployApp(ctx, app, req)
}

func (r *remoteControlPlane) StopApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return r.client.StopApp(ctx, app)
}

func (r *remoteControlPlane) StartApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return r.client.StartApp(ctx, app)
}

func (r *remoteControlPlane) RestartApp(ctx context.Context, app, service string) (*dto.AppDeployResponse, string, error) {
	return r.client.RestartApp(ctx, app, service)
}

func (r *remoteControlPlane) RemoveApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error) {
	return r.client.RemoveApp(ctx, app)
}

func (r *remoteControlPlane) OperationByKey(ctx context.Context, app, key string) (*dto.AppDeployResponse, error) {
	return r.client.OperationByKey(ctx, app, key)
}

func (r *remoteControlPlane) SetAppSecrets(ctx context.Context, app string, req dto.AppSecretSetRequest) error {
	return r.client.SetAppSecrets(ctx, app, req)
}

func (r *remoteControlPlane) DeleteAppSecret(ctx context.Context, app string, req dto.AppSecretDeleteRequest) error {
	return r.client.DeleteAppSecret(ctx, app, req)
}

func (r *remoteControlPlane) ListSecrets(ctx context.Context, secretDomain string) (*remote.SecretsListResult, error) {
	return r.client.ListSecretsWithAttachments(ctx, secretDomain)
}

func (r *remoteControlPlane) SetSecrets(ctx context.Context, secretDomain string, secrets map[string]string) error {
	return r.client.SetSecrets(ctx, secretDomain, secrets)
}

func (r *remoteControlPlane) DeleteSecret(ctx context.Context, secretDomain, key string) error {
	return r.client.DeleteSecret(ctx, secretDomain, key)
}

func (r *remoteControlPlane) GetStatus(ctx context.Context) (*remote.Status, error) {
	return r.client.GetStatus(ctx)
}

func (r *remoteControlPlane) GetTLSStatus(ctx context.Context) (*dto.TLSStatusResponse, error) {
	return r.client.GetTLSStatus(ctx)
}

func (r *remoteControlPlane) GetTrafficStatus(ctx context.Context) (*dto.TrafficStatusResponse, error) {
	return r.client.GetTrafficStatus(ctx)
}

func (r *remoteControlPlane) Reload(ctx context.Context) error {
	return r.client.Reload(ctx)
}

func (r *remoteControlPlane) ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error) {
	return r.client.ListNetworks(ctx)
}

func (r *remoteControlPlane) GetConfig(ctx context.Context) (*remote.Config, error) {
	return r.client.GetConfig(ctx)
}

func (r *remoteControlPlane) ListTags(ctx context.Context, repository string) ([]string, error) {
	return r.client.ListTags(ctx, repository)
}

func (r *remoteControlPlane) ListBackups(ctx context.Context, app string) ([]dto.BackupJob, error) {
	return r.client.ListBackups(ctx, app)
}

func (r *remoteControlPlane) BackupStatus(ctx context.Context) ([]dto.BackupJob, error) {
	return r.client.BackupStatus(ctx)
}

func (r *remoteControlPlane) RunBackup(ctx context.Context, app, service, database string) (*dto.BackupRunResponse, error) {
	return r.client.RunBackup(ctx, app, service, database)
}

func (r *remoteControlPlane) ListVolumeBackups(ctx context.Context, app string) ([]dto.VolumeBackupJob, error) {
	jobs, err := r.client.ListVolumeBackups(ctx, app)
	if err != nil {
		return nil, fmt.Errorf("list volume backups: %w", err)
	}
	return jobs, nil
}

func (r *remoteControlPlane) VolumeBackupStatus(ctx context.Context) ([]dto.VolumeBackupJob, error) {
	jobs, err := r.client.VolumeBackupStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("get volume backup status: %w", err)
	}
	return jobs, nil
}

func (r *remoteControlPlane) RunVolumeBackups(ctx context.Context, app, service, volume string) (*dto.VolumeBackupRunResponse, error) {
	result, err := r.client.RunVolumeBackups(ctx, app, service, volume)
	if err != nil {
		return result, fmt.Errorf("run volume backups: %w", err)
	}
	return result, nil
}

func (r *remoteControlPlane) GetProcessLogs(ctx context.Context, lines int) ([]string, error) {
	return r.client.GetProcessLogs(ctx, lines)
}

func (r *remoteControlPlane) GetContainerLogs(ctx context.Context, logDomain string, lines int) ([]string, error) {
	return r.client.GetContainerLogs(ctx, logDomain, lines)
}

func (r *remoteControlPlane) StreamProcessLogs(ctx context.Context, lines int) (<-chan string, error) {
	return r.client.StreamProcessLogs(ctx, lines)
}

func (r *remoteControlPlane) StreamContainerLogs(ctx context.Context, logDomain string, lines int) (<-chan string, error) {
	return r.client.StreamContainerLogs(ctx, logDomain, lines)
}

func (r *remoteControlPlane) ListVolumes(ctx context.Context) ([]dto.Volume, error) {
	return r.client.ListVolumes(ctx)
}

func (r *remoteControlPlane) PruneVolumes(ctx context.Context, req dto.VolumePruneRequest) (*dto.VolumePruneResponse, error) {
	return r.client.PruneVolumes(ctx, req)
}
