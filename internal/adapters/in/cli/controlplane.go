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

	ListBackups(ctx context.Context, backupDomain string) ([]dto.BackupJob, error)
	BackupStatus(ctx context.Context) ([]dto.BackupJob, error)
	RunBackup(ctx context.Context, backupDomain, dbName string) (*dto.BackupRunResponse, error)
	DetectDatabases(ctx context.Context, backupDomain string) ([]dto.DatabaseInfo, error)
	ListVolumeBackups(ctx context.Context, backupDomain string) ([]dto.VolumeBackupJob, error)
	VolumeBackupStatus(ctx context.Context) ([]dto.VolumeBackupJob, error)
	RunVolumeBackups(ctx context.Context, backupDomain, volumeName string) (*dto.VolumeBackupRunResponse, error)

	GetProcessLogs(ctx context.Context, lines int) ([]string, error)
	GetContainerLogs(ctx context.Context, logDomain string, lines int) ([]string, error)
	StreamProcessLogs(ctx context.Context, lines int) (<-chan string, error)
	StreamContainerLogs(ctx context.Context, logDomain string, lines int) (<-chan string, error)

	ListVolumes(ctx context.Context) ([]dto.Volume, error)
	PruneVolumes(ctx context.Context, req dto.VolumePruneRequest) (*dto.VolumePruneResponse, error)
}
