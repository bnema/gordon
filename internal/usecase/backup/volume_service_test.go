package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"bnema.dev/zerowrap"
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
	mu        sync.Mutex
	err       error
	requested []domain.VolumeArchiveRequest
	// hold, when set, keeps each export open until released so tests can
	// observe how many run at once.
	hold     chan struct{}
	inFlight int
	peak     int
}

func (f *fakeVolumeArchiveExporter) ExportVolumeArchive(_ context.Context, request domain.VolumeArchiveRequest) (*domain.VolumeArchiveResult, error) {
	f.mu.Lock()
	f.requested = append(f.requested, request)
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	hold := f.hold
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if hold != nil {
		<-hold
	}
	if f.err != nil {
		return nil, f.err
	}
	return &domain.VolumeArchiveResult{Stream: io.NopCloser(bytes.NewReader([]byte("archive")))}, nil
}

type fakeVolumeBackupStorage struct {
	mu        sync.Mutex
	stored    []domain.VolumeBackupJob
	retention []string
	jobs      []domain.VolumeBackupJob
	listErr   error
	storeErr  error
}

// StoreVolumeArchive fails on a cancelled context like a real multipart
// upload does.
func (f *fakeVolumeBackupStorage) StoreVolumeArchive(ctx context.Context, job domain.VolumeBackupJob, data io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := io.Copy(io.Discard, data); err != nil {
		return "", err
	}
	if f.storeErr != nil {
		return "", f.storeErr
	}
	f.mu.Lock()
	f.stored = append(f.stored, job)
	f.mu.Unlock()
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

// LatestVolumeArchives returns the configured jobs as-is; the S3 adapter
// owns the newest-per-volume selection.
func (f *fakeVolumeBackupStorage) LatestVolumeArchives(context.Context) ([]domain.VolumeBackupJob, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.jobs, nil
}

func (f *fakeVolumeBackupStorage) DeleteVolumeArchive(context.Context, string) error { return nil }

func (f *fakeVolumeBackupStorage) ApplyVolumeRetention(_ context.Context, app string, _ domain.VolumeBackupRetentionPolicy) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/var/lib/data"),
	}, "ctr-api"), true, nil).Once()

	exporter := &fakeVolumeArchiveExporter{}
	storage := &fakeVolumeBackupStorage{}
	svc := volumeTestService(t, state, exporter, storage, true)

	job, err := runVolumeBackupToEnd(t, svc, "shop", "api", "data")
	require.NoError(t, err)
	jobs := []domain.VolumeBackupJob{job}
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

	_, err := svc.StartVolumeBackup(ctx, "shop", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "api/data")
	assert.Contains(t, err.Error(), "web/uploads")

	_, err = svc.StartVolumeBackup(ctx, "shop", "api", "missing")
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

// TestVolumeService_RunVolumeBackupsForSchedule_HonorsMaxConcurrency proves
// the scheduled pass runs targets in parallel, never more than
// MaxConcurrency at once, and still backs up every target.
func TestVolumeService_RunVolumeBackupsForSchedule_HonorsMaxConcurrency(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprintf("max_concurrency=%d", limit), func(t *testing.T) {
			ctx := context.Background()
			state := outmocks.NewMockAppStateReader(t)
			state.EXPECT().ListApps(mock.Anything).Return([]string{"shop"}, nil).Once()
			state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
			state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
				"a": volumeSpec("a", "data-a", "/a"),
				"b": volumeSpec("b", "data-b", "/b"),
				"c": volumeSpec("c", "data-c", "/c"),
			}, "ctr"), true, nil).Once()

			hold := make(chan struct{})
			exporter := &fakeVolumeArchiveExporter{hold: hold}
			storage := &fakeVolumeBackupStorage{}
			svc := NewVolumeService(exporter, storage, domain.VolumeBackupConfig{
				Enabled:        true,
				Compression:    domain.VolumeBackupCompressionGzip,
				Retention:      domain.VolumeBackupRetentionPolicy{Keep: 2},
				Timeout:        time.Minute,
				MaxConcurrency: limit,
				HelperImage:    "helper:latest",
			}, testLogger()).WithAppState(state)

			done := make(chan error, 1)
			go func() { done <- svc.RunVolumeBackupsForSchedule(ctx, domain.ScheduleDaily) }()

			// Wait until the limit is reached, then confirm it is never exceeded.
			require.Eventually(t, func() bool {
				exporter.mu.Lock()
				defer exporter.mu.Unlock()
				return exporter.inFlight == limit
			}, 2*time.Second, time.Millisecond)
			time.Sleep(20 * time.Millisecond)
			close(hold)

			require.NoError(t, <-done)
			assert.Equal(t, limit, exporter.peak)
			assert.Len(t, storage.stored, 3)
			assert.Equal(t, []string{"shop"}, storage.retention)
		})
	}
}

// TestVolumeService_DisabledDoesNothing proves a disabled backup
// configuration runs nothing at all.
func TestVolumeService_DisabledDoesNothing(t *testing.T) {
	state := outmocks.NewMockAppStateReader(t)
	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{}, &fakeVolumeBackupStorage{}, false)

	_, err := svc.StartVolumeBackup(context.Background(), "shop", "", "")
	require.ErrorIs(t, err, domain.ErrVolumeBackupUnavailable)
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
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/data"),
	}, "ctr-api"), true, nil).Once()

	storage := &fakeVolumeBackupStorage{}
	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{err: errors.New("helper failed")}, storage, true)

	job, err := runVolumeBackupToEnd(t, svc, "shop", "api", "data")
	require.Error(t, err)
	assert.Equal(t, domain.BackupStatusFailed, job.Status)
	assert.Contains(t, job.Error, "helper failed")
	assert.Empty(t, storage.stored)
}

// TestVolumeService_StartVolumeBackup_OutlivesTheCaller is the regression
// for a manual backup aborted when its HTTP client disconnected: the
// caller's context is cancelled mid-export and the archive still uploads.
func TestVolumeService_StartVolumeBackup_OutlivesTheCaller(t *testing.T) {
	state := outmocks.NewMockAppStateReader(t)
	expectShopVolume(state)
	hold := make(chan struct{})
	exporter := &fakeVolumeArchiveExporter{hold: hold}
	storage := &fakeVolumeBackupStorage{}
	svc := volumeTestService(t, state, exporter, storage, true)

	ctx, cancel := context.WithCancel(context.Background())
	job, err := svc.StartVolumeBackup(ctx, "shop", "api", "data")
	require.NoError(t, err)
	assert.Equal(t, domain.BackupStatusRunning, job.Status)
	require.NotEmpty(t, job.ID)

	status, err := svc.VolumeBackupStatus(context.Background())
	require.NoError(t, err)
	require.Len(t, status, 1)
	assert.Equal(t, job.ID, status[0].ID)
	assert.Equal(t, domain.BackupStatusRunning, status[0].Status)

	cancel()
	close(hold)

	done := awaitRun(t, svc.VolumeBackupRun, job.ID, func(j domain.VolumeBackupJob) domain.BackupJobStatus { return j.Status })
	assert.Equal(t, domain.BackupStatusCompleted, done.Status, done.Error)
	storage.mu.Lock()
	defer storage.mu.Unlock()
	assert.Len(t, storage.stored, 1)
	assert.Equal(t, []string{"shop"}, storage.retention)
}

// TestVolumeService_StartVolumeBackup_ReusesTheRunningJob proves a second
// start while the target is being backed up returns the same run instead
// of starting a second upload.
func TestVolumeService_StartVolumeBackup_ReusesTheRunningJob(t *testing.T) {
	state := outmocks.NewMockAppStateReader(t)
	expectShopVolume(state)
	expectShopVolume(state)
	hold := make(chan struct{})
	exporter := &fakeVolumeArchiveExporter{hold: hold}
	svc := volumeTestService(t, state, exporter, &fakeVolumeBackupStorage{}, true)

	first, err := svc.StartVolumeBackup(context.Background(), "shop", "api", "data")
	require.NoError(t, err)
	second, err := svc.StartVolumeBackup(context.Background(), "shop", "api", "data")
	require.NoError(t, err)
	assert.Equal(t, first.ID, second.ID)

	close(hold)
	awaitRun(t, svc.VolumeBackupRun, first.ID, func(j domain.VolumeBackupJob) domain.BackupJobStatus { return j.Status })
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	assert.Len(t, exporter.requested, 1)
}

// TestVolumeService_VolumeBackupRun_UnknownID proves an unknown run is
// reported with the not-found sentinel.
func TestVolumeService_VolumeBackupRun_UnknownID(t *testing.T) {
	svc := volumeTestService(t, outmocks.NewMockAppStateReader(t), &fakeVolumeArchiveExporter{}, &fakeVolumeBackupStorage{}, true)
	_, err := svc.VolumeBackupRun(context.Background(), "nope")
	require.ErrorIs(t, err, domain.ErrBackupRunNotFound)
}
