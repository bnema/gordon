package backup

import (
	"bytes"
	"context"
	"errors"
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
	err       error
	requested []domain.VolumeArchiveRequest
}

func (f *fakeVolumeArchiveExporter) ExportVolumeArchive(_ context.Context, request domain.VolumeArchiveRequest) (*domain.VolumeArchiveResult, error) {
	f.requested = append(f.requested, request)
	if f.err != nil {
		return nil, f.err
	}
	return &domain.VolumeArchiveResult{Stream: io.NopCloser(bytes.NewReader([]byte("archive")))}, nil
}

type fakeVolumeBackupStorage struct {
	stored    []domain.VolumeBackupJob
	retention []string
	jobs      []domain.VolumeBackupJob
	listErr   error
	storeErr  error
}

func (f *fakeVolumeBackupStorage) StoreVolumeArchive(_ context.Context, job domain.VolumeBackupJob, data io.Reader) (string, error) {
	if _, err := io.Copy(io.Discard, data); err != nil {
		return "", err
	}
	if f.storeErr != nil {
		return "", f.storeErr
	}
	f.stored = append(f.stored, job)
	return "s3://bucket/" + job.App + "/" + job.VolumeName, nil
}

func (f *fakeVolumeBackupStorage) GetVolumeArchive(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (f *fakeVolumeBackupStorage) ListVolumeArchives(context.Context, string) ([]domain.VolumeBackupJob, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.jobs, nil
}

func (f *fakeVolumeBackupStorage) DeleteVolumeArchive(context.Context, string) error { return nil }

func (f *fakeVolumeBackupStorage) ApplyVolumeRetention(_ context.Context, app string, _ domain.VolumeBackupRetentionPolicy) (int, error) {
	f.retention = append(f.retention, app)
	return 0, nil
}

// volumeSpec declares one volume and references it from the service's
// backup declaration.
func volumeSpec(service, volume, path string) domain.AppService {
	return domain.AppService{
		Name:    service,
		Image:   "app:1",
		Volumes: []domain.AppVolume{{Name: volume, Path: path}},
		Backup:  domain.AppBackup{Volume: []string{volume}},
	}
}

func volumeTestService(t *testing.T, state *outmocks.MockAppStateReader, exporter *fakeVolumeArchiveExporter, storage *fakeVolumeBackupStorage, enabled bool) *VolumeService {
	t.Helper()
	return NewVolumeService(exporter, storage, domain.VolumeBackupConfig{
		Enabled:     enabled,
		Compression: domain.VolumeBackupCompressionGzip,
		Retention:   domain.VolumeBackupRetentionPolicy{Keep: 2},
		Timeout:     time.Minute,
		HelperImage: "helper:latest",
	}, testLogger()).WithAppState(state)
}

// TestVolumeService_TargetsResolveFromDeclarations proves a volume target
// comes from the ACTIVE declaration, with the runtime volume name and
// mount path the exporter needs. No container label is consulted.
func TestVolumeService_TargetsResolveFromDeclarations(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/var/lib/data"),
	}, "ctr-api"), true, nil).Once()

	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{}, &fakeVolumeBackupStorage{}, true)
	targets, err := svc.VolumeTargets(ctx, "shop")
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "shop", targets[0].App)
	assert.Equal(t, "api", targets[0].Service)
	assert.Equal(t, "data", targets[0].VolumeName)
	assert.Equal(t, domain.RuntimeVolumeName("shop", "api", "data"), targets[0].RuntimeVolumeName)
	assert.Equal(t, "/var/lib/data", targets[0].MountPath)
}

// TestVolumeService_RunVolumeBackups_ExportsTheDeclaredVolume proves the
// archive export targets the runtime volume and the artifact is stored
// under the app identity.
func TestVolumeService_RunVolumeBackups_ExportsTheDeclaredVolume(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/var/lib/data"),
	}, "ctr-api"), true, nil).Once()

	exporter := &fakeVolumeArchiveExporter{}
	storage := &fakeVolumeBackupStorage{}
	svc := volumeTestService(t, state, exporter, storage, true)

	jobs, err := svc.RunVolumeBackups(ctx, "shop", "api", "data")
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, domain.BackupStatusCompleted, jobs[0].Status)
	assert.Equal(t, "shop", jobs[0].App)
	assert.Equal(t, "api", jobs[0].Service)
	assert.Equal(t, "data", jobs[0].VolumeName)
	assert.Equal(t, int64(len("archive")), jobs[0].SizeBytes)

	require.Len(t, exporter.requested, 1)
	assert.Equal(t, domain.RuntimeVolumeName("shop", "api", "data"), exporter.requested[0].VolumeName)
	assert.Equal(t, "/var/lib/data", exporter.requested[0].MountPath)
	require.Len(t, storage.stored, 1)
	assert.Equal(t, "shop", storage.stored[0].App)
	assert.Equal(t, []string{"shop"}, storage.retention)
	assert.Contains(t, jobs[0].ArtifactRef, "shop")
}

// TestVolumeService_RunVolumeBackups_SelectorRules proves the explicit
// selector rules for volume backups too.
func TestVolumeService_RunVolumeBackups_SelectorRules(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil)
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/data"),
		"web": volumeSpec("web", "uploads", "/uploads"),
	}, "ctr-x"), true, nil)

	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{}, &fakeVolumeBackupStorage{}, true)

	_, err := svc.RunVolumeBackups(ctx, "shop", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "api/data")
	assert.Contains(t, err.Error(), "web/uploads")

	_, err = svc.RunVolumeBackups(ctx, "shop", "api", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestVolumeService_RunVolumeBackupsForSchedule_RunsEveryDeclaredTarget
// proves the scheduled pass enumerates declared targets and applies
// retention per app.
func TestVolumeService_RunVolumeBackupsForSchedule_RunsEveryDeclaredTarget(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"shop"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/data"),
	}, "ctr-api"), true, nil).Once()

	exporter := &fakeVolumeArchiveExporter{}
	storage := &fakeVolumeBackupStorage{}
	svc := volumeTestService(t, state, exporter, storage, true)

	require.NoError(t, svc.RunVolumeBackupsForSchedule(ctx, domain.ScheduleDaily))
	require.Len(t, storage.stored, 1)
	assert.Equal(t, "data", storage.stored[0].VolumeName)
	assert.Equal(t, []string{"shop"}, storage.retention)
}

// TestVolumeService_DisabledDoesNothing proves a disabled backup
// configuration runs nothing at all.
func TestVolumeService_DisabledDoesNothing(t *testing.T) {
	state := outmocks.NewMockAppStateReader(t)
	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{}, &fakeVolumeBackupStorage{}, false)

	jobs, err := svc.RunVolumeBackups(context.Background(), "shop", "", "")
	require.NoError(t, err)
	assert.Empty(t, jobs)
	require.NoError(t, svc.RunVolumeBackupsForSchedule(context.Background(), domain.ScheduleDaily))
}

// TestVolumeService_ListVolumeBackupsWrapsStorageError proves storage
// failures are reported with context.
func TestVolumeService_ListVolumeBackupsWrapsStorageError(t *testing.T) {
	state := outmocks.NewMockAppStateReader(t)
	storage := &fakeVolumeBackupStorage{listErr: errors.New("boom")}
	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{}, storage, true)

	_, err := svc.ListVolumeBackups(context.Background(), "shop")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list volume archives")
}

// TestVolumeService_FailedExportReportsTheJob proves a failed export keeps
// a failed job with its error and stores nothing.
func TestVolumeService_FailedExportReportsTheJob(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/data"),
	}, "ctr-api"), true, nil).Once()

	storage := &fakeVolumeBackupStorage{}
	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{err: errors.New("helper failed")}, storage, true)

	jobs, err := svc.RunVolumeBackups(ctx, "shop", "api", "data")
	require.Error(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, domain.BackupStatusFailed, jobs[0].Status)
	assert.Contains(t, jobs[0].Error, "helper failed")
	assert.Empty(t, storage.stored)
}
