package backup

import (
	"context"
	"io"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

type databaseBackupRuntimeOnly struct{}

func (databaseBackupRuntimeOnly) ExecInContainer(context.Context, string, []string) (*out.ExecResult, error) {
	return &out.ExecResult{}, nil
}

func (databaseBackupRuntimeOnly) CopyFromContainer(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}

type volumeBackupRuntimeOnly struct{}

func (volumeBackupRuntimeOnly) ListContainers(context.Context, bool) ([]*domain.Container, error) {
	return nil, nil
}

func TestBackupServicesAcceptNarrowRuntimePorts(t *testing.T) {
	storage := &fakeVolumeBackupStorage{}

	logical := NewService(databaseBackupRuntimeOnly{}, nil, nil, domain.BackupConfig{}, zerowrap.Default())
	volume := NewVolumeService(volumeBackupRuntimeOnly{}, fakeVolumeArchiveExporter{}, storage, domain.VolumeBackupConfig{}, testLogger())

	require.NotNil(t, logical)
	jobs, err := volume.RunVolumeBackups(context.Background(), "", "")
	require.NoError(t, err)
	require.Empty(t, jobs)
}

func TestBackupRuntimePortShapeDocumentsRequiredCapabilities(t *testing.T) {
	var _ out.BackupDatabaseRuntime = databaseBackupRuntimeOnly{}
	var _ out.BackupVolumeTargetRuntime = volumeBackupRuntimeOnly{}
}
