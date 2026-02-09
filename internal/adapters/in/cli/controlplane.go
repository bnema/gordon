package cli

import (
	"context"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

// ControlPlane defines command operations available to CLI execution paths.
//
// Remote implementations call admin HTTP APIs.
// Local implementations call services directly in-process.
type ControlPlane interface {
	ListRoutesWithDetails(ctx context.Context) ([]remote.RouteInfo, error)
	GetHealth(ctx context.Context) (map[string]*remote.RouteHealth, error)
	GetRoute(ctx context.Context, routeDomain string) (*domain.Route, error)
	AddRoute(ctx context.Context, route domain.Route) error
	UpdateRoute(ctx context.Context, route domain.Route) error
	RemoveRoute(ctx context.Context, routeDomain string) error

	ListSecretsWithAttachments(ctx context.Context, secretDomain string) (*remote.SecretsListResult, error)
	SetSecrets(ctx context.Context, secretDomain string, secrets map[string]string) error
	DeleteSecret(ctx context.Context, secretDomain, key string) error
	SetAttachmentSecrets(ctx context.Context, domain, service string, secrets map[string]string) error
	DeleteAttachmentSecret(ctx context.Context, domain, service, key string) error

	GetAllAttachmentsConfig(ctx context.Context) (map[string][]string, error)
	GetAttachmentsConfig(ctx context.Context, domainOrGroup string) ([]string, error)
	AddAttachment(ctx context.Context, domainOrGroup, image string) error
	RemoveAttachment(ctx context.Context, domainOrGroup, image string) error

	GetStatus(ctx context.Context) (*remote.Status, error)
	Reload(ctx context.Context) error
	Deploy(ctx context.Context, deployDomain string) (*remote.DeployResult, error)
	Restart(ctx context.Context, restartDomain string, withAttachments bool) (*remote.RestartResult, error)
	ListTags(ctx context.Context, repository string) ([]string, error)

	ListBackups(ctx context.Context, backupDomain string) ([]dto.BackupJob, error)
	BackupStatus(ctx context.Context) ([]dto.BackupJob, error)
	RunBackup(ctx context.Context, backupDomain, dbName string) (*dto.BackupRunResponse, error)
	DetectDatabases(ctx context.Context, backupDomain string) ([]dto.DatabaseInfo, error)

	GetProcessLogs(ctx context.Context, lines int) ([]string, error)
	GetContainerLogs(ctx context.Context, logDomain string, lines int) ([]string, error)
	StreamProcessLogs(ctx context.Context, lines int) (<-chan string, error)
	StreamContainerLogs(ctx context.Context, logDomain string, lines int) (<-chan string, error)
}
