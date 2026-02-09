package cli

import (
	"context"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

type remoteControlPlane struct {
	client *remote.Client
}

func NewRemoteControlPlane(client *remote.Client) ControlPlane {
	return &remoteControlPlane{client: client}
}

func (r *remoteControlPlane) ListRoutesWithDetails(ctx context.Context) ([]remote.RouteInfo, error) {
	return r.client.ListRoutesWithDetails(ctx)
}

func (r *remoteControlPlane) GetHealth(ctx context.Context) (map[string]*remote.RouteHealth, error) {
	return r.client.GetHealth(ctx)
}

func (r *remoteControlPlane) GetRoute(ctx context.Context, routeDomain string) (*domain.Route, error) {
	return r.client.GetRoute(ctx, routeDomain)
}

func (r *remoteControlPlane) AddRoute(ctx context.Context, route domain.Route) error {
	return r.client.AddRoute(ctx, route)
}

func (r *remoteControlPlane) UpdateRoute(ctx context.Context, route domain.Route) error {
	return r.client.UpdateRoute(ctx, route)
}

func (r *remoteControlPlane) RemoveRoute(ctx context.Context, routeDomain string) error {
	return r.client.RemoveRoute(ctx, routeDomain)
}

func (r *remoteControlPlane) ListSecretsWithAttachments(ctx context.Context, secretDomain string) (*remote.SecretsListResult, error) {
	return r.client.ListSecretsWithAttachments(ctx, secretDomain)
}

func (r *remoteControlPlane) SetSecrets(ctx context.Context, secretDomain string, secrets map[string]string) error {
	return r.client.SetSecrets(ctx, secretDomain, secrets)
}

func (r *remoteControlPlane) DeleteSecret(ctx context.Context, secretDomain, key string) error {
	return r.client.DeleteSecret(ctx, secretDomain, key)
}

func (r *remoteControlPlane) SetAttachmentSecrets(ctx context.Context, domainName, service string, secrets map[string]string) error {
	return r.client.SetAttachmentSecrets(ctx, domainName, service, secrets)
}

func (r *remoteControlPlane) DeleteAttachmentSecret(ctx context.Context, domainName, service, key string) error {
	return r.client.DeleteAttachmentSecret(ctx, domainName, service, key)
}

func (r *remoteControlPlane) GetAllAttachmentsConfig(ctx context.Context) (map[string][]string, error) {
	return r.client.GetAllAttachmentsConfig(ctx)
}

func (r *remoteControlPlane) GetAttachmentsConfig(ctx context.Context, domainOrGroup string) ([]string, error) {
	return r.client.GetAttachmentsConfig(ctx, domainOrGroup)
}

func (r *remoteControlPlane) AddAttachment(ctx context.Context, domainOrGroup, image string) error {
	return r.client.AddAttachment(ctx, domainOrGroup, image)
}

func (r *remoteControlPlane) RemoveAttachment(ctx context.Context, domainOrGroup, image string) error {
	return r.client.RemoveAttachment(ctx, domainOrGroup, image)
}

func (r *remoteControlPlane) GetStatus(ctx context.Context) (*remote.Status, error) {
	return r.client.GetStatus(ctx)
}

func (r *remoteControlPlane) Reload(ctx context.Context) error {
	return r.client.Reload(ctx)
}

func (r *remoteControlPlane) Deploy(ctx context.Context, deployDomain string) (*remote.DeployResult, error) {
	return r.client.Deploy(ctx, deployDomain)
}

func (r *remoteControlPlane) Restart(ctx context.Context, restartDomain string, withAttachments bool) (*remote.RestartResult, error) {
	return r.client.Restart(ctx, restartDomain, withAttachments)
}

func (r *remoteControlPlane) ListTags(ctx context.Context, repository string) ([]string, error) {
	return r.client.ListTags(ctx, repository)
}

func (r *remoteControlPlane) ListBackups(ctx context.Context, backupDomain string) ([]dto.BackupJob, error) {
	return r.client.ListBackups(ctx, backupDomain)
}

func (r *remoteControlPlane) BackupStatus(ctx context.Context) ([]dto.BackupJob, error) {
	return r.client.BackupStatus(ctx)
}

func (r *remoteControlPlane) RunBackup(ctx context.Context, backupDomain, dbName string) (*dto.BackupRunResponse, error) {
	return r.client.RunBackup(ctx, backupDomain, dbName)
}

func (r *remoteControlPlane) DetectDatabases(ctx context.Context, backupDomain string) ([]dto.DatabaseInfo, error) {
	return r.client.DetectDatabases(ctx, backupDomain)
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
