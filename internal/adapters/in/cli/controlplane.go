package cli

import (
	"context"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/domain"
)

// ControlPlane defines command operations available to CLI execution paths.
// Both explicit remote and local-daemon implementations call admin HTTP APIs.
type ControlPlane interface {
	// App lifecycle and reads. App mutations are daemon-owned for both
	// the explicit remote and the owner-only local admin socket.
	ApplyApp(ctx context.Context, req dto.AppApplyRequest) (*dto.AppApplyResponse, error)
	ListApps(ctx context.Context) ([]dto.AppSummaryDTO, error)
	ShowApp(ctx context.Context, app string) (*dto.AppShowResponse, error)
	DiffApp(ctx context.Context, app string) (*dto.AppDiffResponse, error)
	DeployApp(ctx context.Context, app string, req dto.AppDeployRequest) (*dto.AppDeployResponse, string, error)
	StopApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error)
	StartApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error)
	RestartApp(ctx context.Context, app, service string) (*dto.AppDeployResponse, string, error)
	RemoveApp(ctx context.Context, app string) (*dto.AppDeployResponse, string, error)
	OperationByKey(ctx context.Context, app, key string) (*dto.AppDeployResponse, error)
	SetAppSecrets(ctx context.Context, app string, req dto.AppSecretSetRequest) error
	DeleteAppSecret(ctx context.Context, app string, req dto.AppSecretDeleteRequest) error

	ListSecrets(ctx context.Context, secretDomain string) (*remote.SecretsListResult, error)
	SetSecrets(ctx context.Context, secretDomain string, secrets map[string]string) error
	DeleteSecret(ctx context.Context, secretDomain, key string) error

	GetStatus(ctx context.Context) (*remote.Status, error)
	GetTLSStatus(ctx context.Context) (*dto.TLSStatusResponse, error)
	GetTrafficStatus(ctx context.Context) (*dto.TrafficStatusResponse, error)
	Reload(ctx context.Context) error
	ListNetworks(ctx context.Context) ([]*domain.NetworkInfo, error)
	GetConfig(ctx context.Context) (*remote.Config, error)
	ListTags(ctx context.Context, repository string) ([]string, error)

	ListBackups(ctx context.Context, app string) ([]dto.BackupJob, error)
	BackupStatus(ctx context.Context) ([]dto.BackupJob, error)
	RunBackup(ctx context.Context, app, service, database string) (*dto.BackupRunResponse, error)
	ListVolumeBackups(ctx context.Context, app string) ([]dto.VolumeBackupJob, error)
	VolumeBackupStatus(ctx context.Context) ([]dto.VolumeBackupJob, error)
	RunVolumeBackups(ctx context.Context, app, service, volume string) (*dto.VolumeBackupRunResponse, error)

	GetProcessLogs(ctx context.Context, lines int) ([]string, error)
	GetContainerLogs(ctx context.Context, logDomain string, lines int) ([]string, error)
	StreamProcessLogs(ctx context.Context, lines int) (<-chan string, error)
	StreamContainerLogs(ctx context.Context, logDomain string, lines int) (<-chan string, error)

	ListVolumes(ctx context.Context) ([]dto.Volume, error)
	PruneVolumes(ctx context.Context, req dto.VolumePruneRequest) (*dto.VolumePruneResponse, error)
}
