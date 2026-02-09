package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/app"
	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/domain"
)

type localControlPlane struct {
	configSvc    in.ConfigService
	secretSvc    in.SecretService
	containerSvc in.ContainerService
	backupSvc    in.BackupService
	registrySvc  in.RegistryService
	healthSvc    in.HealthService
	logSvc       in.LogService
}

func NewLocalControlPlane(kernel *app.Kernel) ControlPlane {
	if kernel == nil {
		return &localControlPlane{}
	}

	return &localControlPlane{
		configSvc:    kernel.Config(),
		secretSvc:    kernel.Secrets(),
		containerSvc: kernel.Container(),
		backupSvc:    kernel.Backup(),
		registrySvc:  kernel.Registry(),
		healthSvc:    kernel.Health(),
		logSvc:       kernel.Logs(),
	}
}

func (l *localControlPlane) ListRoutesWithDetails(ctx context.Context) ([]remote.RouteInfo, error) {
	if l.containerSvc != nil {
		return toRemoteRouteInfos(l.containerSvc.ListRoutesWithDetails(ctx)), nil
	}

	if l.configSvc == nil {
		return nil, fmt.Errorf("local config service unavailable")
	}

	routes := l.configSvc.GetRoutes(ctx)
	infos := make([]remote.RouteInfo, 0, len(routes))
	for _, route := range routes {
		infos = append(infos, remote.RouteInfo{Domain: route.Domain, Image: route.Image})
	}
	return infos, nil
}

func (l *localControlPlane) GetHealth(ctx context.Context) (map[string]*remote.RouteHealth, error) {
	if l.healthSvc == nil {
		return map[string]*remote.RouteHealth{}, nil
	}

	health := l.healthSvc.CheckAllRoutes(ctx)
	result := make(map[string]*remote.RouteHealth, len(health))
	for domainName, h := range health {
		if h == nil {
			continue
		}
		result[domainName] = &remote.RouteHealth{
			ContainerStatus: h.ContainerStatus,
			HTTPStatus:      h.HTTPStatus,
			ResponseTimeMs:  h.ResponseTimeMs,
			Healthy:         h.Healthy,
			Error:           h.Error,
		}
	}
	return result, nil
}

func (l *localControlPlane) GetRoute(ctx context.Context, routeDomain string) (*domain.Route, error) {
	if l.configSvc == nil {
		return nil, fmt.Errorf("local config service unavailable")
	}
	return l.configSvc.GetRoute(ctx, routeDomain)
}

func (l *localControlPlane) AddRoute(ctx context.Context, route domain.Route) error {
	if l.configSvc == nil {
		return fmt.Errorf("local config service unavailable")
	}
	return l.configSvc.AddRoute(ctx, route)
}

func (l *localControlPlane) UpdateRoute(ctx context.Context, route domain.Route) error {
	if l.configSvc == nil {
		return fmt.Errorf("local config service unavailable")
	}
	return l.configSvc.UpdateRoute(ctx, route)
}

func (l *localControlPlane) RemoveRoute(ctx context.Context, routeDomain string) error {
	if l.configSvc == nil {
		return fmt.Errorf("local config service unavailable")
	}
	return l.configSvc.RemoveRoute(ctx, routeDomain)
}

func (l *localControlPlane) ListSecretsWithAttachments(ctx context.Context, secretDomain string) (*remote.SecretsListResult, error) {
	if l.secretSvc == nil {
		return nil, fmt.Errorf("local secret service unavailable")
	}

	keys, attachments, err := l.secretSvc.ListKeysWithAttachments(ctx, secretDomain)
	if err != nil {
		return nil, err
	}

	result := &remote.SecretsListResult{
		Domain: secretDomain,
		Keys:   keys,
	}
	for _, att := range attachments {
		result.Attachments = append(result.Attachments, remote.AttachmentSecrets{
			Service: att.Service,
			Keys:    att.Keys,
		})
	}

	return result, nil
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

func (l *localControlPlane) SetAttachmentSecrets(ctx context.Context, domainName, service string, secrets map[string]string) error {
	if l.secretSvc == nil {
		return fmt.Errorf("local secret service unavailable")
	}
	return l.secretSvc.SetAttachment(ctx, domainName, service, secrets)
}

func (l *localControlPlane) DeleteAttachmentSecret(ctx context.Context, domainName, service, key string) error {
	if l.secretSvc == nil {
		return fmt.Errorf("local secret service unavailable")
	}
	return l.secretSvc.DeleteAttachment(ctx, domainName, service, key)
}

func (l *localControlPlane) GetAllAttachmentsConfig(ctx context.Context) (map[string][]string, error) {
	if l.configSvc == nil {
		return nil, fmt.Errorf("local config service unavailable")
	}
	return l.configSvc.GetAllAttachments(ctx), nil
}

func (l *localControlPlane) GetAttachmentsConfig(ctx context.Context, domainOrGroup string) ([]string, error) {
	if l.configSvc == nil {
		return nil, fmt.Errorf("local config service unavailable")
	}
	return l.configSvc.GetAttachmentsFor(ctx, domainOrGroup)
}

func (l *localControlPlane) AddAttachment(ctx context.Context, domainOrGroup, image string) error {
	if l.configSvc == nil {
		return fmt.Errorf("local config service unavailable")
	}
	return l.configSvc.AddAttachment(ctx, domainOrGroup, image)
}

func (l *localControlPlane) RemoveAttachment(ctx context.Context, domainOrGroup, image string) error {
	if l.configSvc == nil {
		return fmt.Errorf("local config service unavailable")
	}
	return l.configSvc.RemoveAttachment(ctx, domainOrGroup, image)
}

func (l *localControlPlane) GetStatus(ctx context.Context) (*remote.Status, error) {
	if l.configSvc == nil {
		return nil, fmt.Errorf("local config service unavailable")
	}

	status := &remote.Status{
		Routes:           len(l.configSvc.GetRoutes(ctx)),
		RegistryDomain:   l.configSvc.GetRegistryDomain(),
		RegistryPort:     l.configSvc.GetRegistryPort(),
		ServerPort:       l.configSvc.GetServerPort(),
		AutoRoute:        l.configSvc.IsAutoRouteEnabled(),
		NetworkIsolation: l.configSvc.IsNetworkIsolationEnabled(),
		ContainerStatus:  map[string]string{},
	}

	if l.containerSvc != nil {
		for domainName, container := range l.containerSvc.List(ctx) {
			if container == nil {
				continue
			}
			status.ContainerStatus[domainName] = container.Status
		}
	}

	return status, nil
}

func (l *localControlPlane) Reload(_ context.Context) error {
	return app.SendReloadSignal()
}

func (l *localControlPlane) Deploy(ctx context.Context, deployDomain string) (*remote.DeployResult, error) {
	if l.containerSvc != nil && l.configSvc != nil {
		route, err := l.configSvc.GetRoute(ctx, deployDomain)
		if err != nil {
			return nil, err
		}
		container, err := l.containerSvc.Deploy(ctx, *route)
		if err != nil {
			return nil, err
		}
		result := &remote.DeployResult{Status: "deployed", Domain: deployDomain}
		if container != nil {
			result.ContainerID = container.ID
		}
		return result, nil
	}

	domainName, err := app.SendDeploySignal(deployDomain)
	if err != nil {
		return nil, err
	}
	return &remote.DeployResult{Status: "queued", Domain: domainName}, nil
}

func (l *localControlPlane) Restart(ctx context.Context, restartDomain string, withAttachments bool) (*remote.RestartResult, error) {
	if l.containerSvc == nil {
		if withAttachments {
			return nil, fmt.Errorf("local restart with attachments requires active local container service")
		}
		domainName, err := app.SendDeploySignal(restartDomain)
		if err != nil {
			return nil, err
		}
		return &remote.RestartResult{Status: "queued", Domain: domainName}, nil
	}

	if err := l.containerSvc.SyncContainers(ctx); err != nil {
		return nil, err
	}
	if err := l.containerSvc.Restart(ctx, restartDomain, withAttachments); err != nil {
		return nil, err
	}

	return &remote.RestartResult{Status: "restarted", Domain: restartDomain}, nil
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

func toRemoteRouteInfos(routes []domain.RouteInfo) []remote.RouteInfo {
	out := make([]remote.RouteInfo, 0, len(routes))
	for _, route := range routes {
		attachments := make([]dto.Attachment, 0, len(route.Attachments))
		for _, attachment := range route.Attachments {
			attachments = append(attachments, dto.Attachment{
				Name:        attachment.Name,
				Image:       attachment.Image,
				ContainerID: attachment.ContainerID,
				Status:      attachment.Status,
				Network:     attachment.Network,
			})
		}

		out = append(out, remote.RouteInfo{
			Domain:          route.Domain,
			Image:           route.Image,
			ContainerID:     route.ContainerID,
			ContainerStatus: route.ContainerStatus,
			Network:         route.Network,
			Attachments:     attachments,
		})
	}
	return out
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
