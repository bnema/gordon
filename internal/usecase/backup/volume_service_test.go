package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func testLogger() zerowrap.Logger {
	return zerowrap.New(zerowrap.Config{Level: "disabled", Output: io.Discard})
}

type fakeVolumeArchiveExporter struct {
	err error
}

func (f fakeVolumeArchiveExporter) ExportVolumeArchive(context.Context, domain.VolumeArchiveRequest) (*domain.VolumeArchiveResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &domain.VolumeArchiveResult{Stream: io.NopCloser(bytes.NewReader([]byte("archive")))}, nil
}

type fakeVolumeBackupStorage struct {
	stored    []domain.VolumeBackupJob
	retention []string
}

func (f *fakeVolumeBackupStorage) StoreVolumeArchive(_ context.Context, job domain.VolumeBackupJob, data io.Reader) (string, error) {
	if _, err := io.Copy(io.Discard, data); err != nil {
		return "", err
	}
	f.stored = append(f.stored, job)
	return "s3://bucket/" + job.VolumeName, nil
}

func (f *fakeVolumeBackupStorage) GetVolumeArchive(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (f *fakeVolumeBackupStorage) ListVolumeArchives(context.Context, string) ([]domain.VolumeBackupJob, error) {
	return nil, nil
}

func (f *fakeVolumeBackupStorage) DeleteVolumeArchive(context.Context, string) error { return nil }

func (f *fakeVolumeBackupStorage) ApplyVolumeRetention(_ context.Context, domainName string, _ domain.VolumeBackupRetentionPolicy) (int, error) {
	f.retention = append(f.retention, domainName)
	return 0, nil
}

func TestVolumeServiceRunVolumeBackups(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	storage := &fakeVolumeBackupStorage{}
	svc := NewVolumeService(runtime, fakeVolumeArchiveExporter{}, storage, domain.VolumeBackupConfig{
		Enabled:        true,
		Compression:    domain.VolumeBackupCompressionGzip,
		Retention:      domain.VolumeBackupRetentionPolicy{Keep: 2},
		Timeout:        time.Minute,
		MaxConcurrency: 1,
		HelperImage:    "helper:latest",
		VolumePrefix:   "gordon",
	}, testLogger())

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{
		{
			ID:   "app",
			Name: "app",
			Labels: map[string]string{
				domain.LabelManaged: "true",
				domain.LabelDomain:  "app.example.com",
			},
			VolumeMounts: []domain.ContainerVolumeMount{{Name: "gordon-app-data", Type: "volume", Destination: "/data"}},
		},
	}, nil)

	jobs, err := svc.RunVolumeBackups(context.Background(), "", "")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, domain.BackupStatusCompleted, jobs[0].Status)
	assert.Equal(t, "s3://bucket/gordon-app-data", jobs[0].ArtifactRef)
	assert.Equal(t, int64(len("archive")), jobs[0].SizeBytes)
	assert.Len(t, storage.stored, 1)
	assert.Equal(t, []string{"app.example.com"}, storage.retention)
}

func TestVolumeServiceRunVolumeBackupsReturnsPartialFailure(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	storage := &fakeVolumeBackupStorage{}
	svc := NewVolumeService(runtime, fakeVolumeArchiveExporter{err: fmt.Errorf("boom")}, storage, domain.VolumeBackupConfig{
		Enabled:        true,
		Compression:    domain.VolumeBackupCompressionGzip,
		Retention:      domain.VolumeBackupRetentionPolicy{Keep: 2},
		Timeout:        time.Minute,
		MaxConcurrency: 1,
		HelperImage:    "helper:latest",
		VolumePrefix:   "gordon",
	}, testLogger())

	runtime.EXPECT().ListContainers(mock.Anything, true).Return([]*domain.Container{
		{
			ID:   "app",
			Name: "app",
			Labels: map[string]string{
				domain.LabelManaged: "true",
				domain.LabelDomain:  "app.example.com",
			},
			VolumeMounts: []domain.ContainerVolumeMount{{Name: "gordon-app-data", Type: "volume", Destination: "/data"}},
		},
	}, nil)

	jobs, err := svc.RunVolumeBackups(context.Background(), "", "")
	require.Error(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, domain.BackupStatusFailed, jobs[0].Status)
	assert.Contains(t, jobs[0].Error, "boom")
	assert.Empty(t, storage.stored)
}

func TestVolumeServiceDisabledDoesNothing(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	svc := NewVolumeService(runtime, fakeVolumeArchiveExporter{}, &fakeVolumeBackupStorage{}, domain.VolumeBackupConfig{}, testLogger())

	jobs, err := svc.RunVolumeBackups(context.Background(), "", "")

	require.NoError(t, err)
	assert.Empty(t, jobs)
}
