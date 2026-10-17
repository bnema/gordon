package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

// recordingMetrics is a concurrency-safe out.BackupMetrics fake.
type recordingMetrics struct {
	mu     sync.Mutex
	runs   []domain.BackupRunRecord
	stored []domain.BackupRunRecord
}

func (r *recordingMetrics) RecordBackupRun(_ context.Context, run domain.BackupRunRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.runs = append(r.runs, run)
}

func (r *recordingMetrics) ObserveStoredBackup(run domain.BackupRunRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stored = append(r.stored, run)
}

var _ out.BackupMetrics = (*recordingMetrics)(nil)

func TestVolumeService_RecordsRunMetrics(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"shop"}, nil).Once()
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/data"),
	}, "ctr-api"), true, nil).Once()

	metrics := &recordingMetrics{}
	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{}, &fakeVolumeBackupStorage{}, true).WithMetrics(metrics)

	require.NoError(t, svc.RunVolumeBackupsForSchedule(ctx, domain.ScheduleDaily))
	require.Len(t, metrics.runs, 1)
	run := metrics.runs[0]
	assert.Equal(t, domain.BackupKindVolume, run.Kind)
	assert.Equal(t, "shop", run.App)
	assert.Equal(t, "api", run.Service)
	assert.Equal(t, "data", run.Target)
	assert.True(t, run.Succeeded)
	assert.Equal(t, int64(len("archive")), run.SizeBytes)
	assert.False(t, run.FinishedAt.IsZero())
}

func TestVolumeService_RecordsFailedRun(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/data"),
	}, "ctr-api"), true, nil).Once()

	metrics := &recordingMetrics{}
	exporter := &fakeVolumeArchiveExporter{err: errors.New("helper failed")}
	svc := volumeTestService(t, state, exporter, &fakeVolumeBackupStorage{}, true).WithMetrics(metrics)

	_, err := svc.RunVolumeBackups(ctx, "shop", "api", "data")
	require.Error(t, err)
	require.Len(t, metrics.runs, 1)
	assert.False(t, metrics.runs[0].Succeeded)
	assert.Equal(t, "data", metrics.runs[0].Target)
}

func TestVolumeService_SeedMetricsFromStoredArchives(t *testing.T) {
	ctx := context.Background()
	started := time.Date(2026, 10, 17, 1, 30, 0, 0, time.UTC)
	storage := &fakeVolumeBackupStorage{jobs: []domain.VolumeBackupJob{
		{App: "shop", Service: "api", VolumeName: "data", Status: domain.BackupStatusCompleted, StartedAt: started, SizeBytes: 42},
	}}
	metrics := &recordingMetrics{}
	svc := volumeTestService(t, outmocks.NewMockAppStateReader(t), &fakeVolumeArchiveExporter{}, storage, true).WithMetrics(metrics)

	require.NoError(t, svc.SeedMetrics(ctx))
	require.Len(t, metrics.stored, 1)
	assert.Equal(t, domain.BackupRunRecord{
		Kind: domain.BackupKindVolume, App: "shop", Service: "api", Target: "data",
		Succeeded: true, SizeBytes: 42, FinishedAt: started,
	}, metrics.stored[0])
	assert.Empty(t, metrics.runs)
}

func TestService_RecordsRunMetrics(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": appSpecWithDatabase("api", "orders", "daily"),
	}, "ctr-api"), true, nil).Once()

	runtime, storage, svc := backupTestService(t, state)
	metrics := &recordingMetrics{}
	svc.WithMetrics(metrics)
	runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.Anything).
		Return(&out.ExecResult{ExitCode: 1, Stderr: []byte("boom")}, nil).Once()
	runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.MatchedBy(func(cmd []string) bool {
		return strings.HasPrefix(cmd[2], "rm -f ")
	})).Return(&out.ExecResult{ExitCode: 0}, nil).Once()

	_, err := svc.RunBackup(ctx, "shop", "api", "orders")
	require.Error(t, err)
	require.Len(t, metrics.runs, 1)
	assert.Equal(t, domain.BackupRunRecord{
		Kind: domain.BackupKindDatabase, App: "shop", Service: "api", Target: "orders",
		Succeeded: false, Duration: metrics.runs[0].Duration, FinishedAt: metrics.runs[0].FinishedAt,
	}, metrics.runs[0])
	_ = storage
}

func TestService_RecordsSuccessfulRun(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": appSpecWithDatabase("api", "orders", "daily"),
	}, "ctr-api"), true, nil).Once()

	runtime, storage, svc := backupTestService(t, state)
	metrics := &recordingMetrics{}
	svc.WithMetrics(metrics)
	runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.Anything).Return(&out.ExecResult{ExitCode: 0}, nil).Twice()
	runtime.EXPECT().CopyFromContainer(mock.Anything, "ctr-api", mock.Anything).
		Return(io.NopCloser(strings.NewReader("dump")), nil).Once()
	storage.EXPECT().Store(mock.Anything, "shop", "api", "orders", domain.BackupSchedule(""), mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, _ domain.BackupSchedule, _ time.Time, data io.Reader) (string, error) {
			_, err := io.Copy(io.Discard, data)
			return "/b/file.bak", err
		}).Once()

	_, err := svc.RunBackup(ctx, "shop", "api", "orders")
	require.NoError(t, err)
	require.Len(t, metrics.runs, 1)
	assert.True(t, metrics.runs[0].Succeeded)
	assert.Equal(t, int64(len("dump")), metrics.runs[0].SizeBytes)
}

func TestService_SeedMetricsFromStoredDumps(t *testing.T) {
	ctx := context.Background()
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"shop"}, nil).Once()
	started := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)

	_, storage, svc := backupTestService(t, state)
	metrics := &recordingMetrics{}
	svc.WithMetrics(metrics)
	storage.EXPECT().List(mock.Anything, "shop", mock.Anything).Return([]domain.BackupJob{{
		App: "shop", Service: "api", DBName: "orders", Status: domain.BackupStatusCompleted,
		StartedAt: started, SizeBytes: 7,
	}}, nil).Once()

	require.NoError(t, svc.SeedMetrics(ctx))
	require.Len(t, metrics.stored, 1)
	assert.Equal(t, domain.BackupRunRecord{
		Kind: domain.BackupKindDatabase, App: "shop", Service: "api", Target: "orders",
		Succeeded: true, SizeBytes: 7, FinishedAt: started,
	}, metrics.stored[0])
}
