package backup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

var (
	shopData   = domain.BackupTargetRef{Kind: domain.BackupKindVolume, App: "shop", Service: "api", Target: "data"}
	shopOrders = domain.BackupTargetRef{Kind: domain.BackupKindDatabase, App: "shop", Service: "api", Target: "orders"}
)

// captureRuns records every RecordBackupRun call on the mock.
func captureRuns(m *outmocks.MockBackupMetrics) *[]domain.BackupRunRecord {
	runs := &[]domain.BackupRunRecord{}
	m.EXPECT().RecordBackupRun(mock.Anything, mock.Anything).Run(func(_ context.Context, run domain.BackupRunRecord) {
		*runs = append(*runs, run)
	})
	return runs
}

func expectShopVolume(state *outmocks.MockAppStateReader) {
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": volumeSpec("api", "data", "/data"),
	}, "ctr-api"), true, nil).Once()
}

// TestVolumeService_RecordsRunMetrics covers the success and failure of a
// volume run: the record carries the declared target and the run's start,
// which is also the time stored archives are named after.
func TestVolumeService_RecordsRunMetrics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		export  error
		success bool
	}{
		{"success", nil, true},
		{"export failure", errors.New("helper failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := outmocks.NewMockAppStateReader(t)
			expectShopVolume(state)
			metrics := outmocks.NewMockBackupMetrics(t)
			runs := captureRuns(metrics)
			storage := &fakeVolumeBackupStorage{}
			svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{err: tc.export}, storage, true).WithMetrics(metrics)

			jobs, _ := svc.RunVolumeBackups(context.Background(), "shop", "api", "data")
			require.Len(t, jobs, 1)
			require.Len(t, *runs, 1)
			run := (*runs)[0]
			assert.Equal(t, shopData, run.BackupTargetRef)
			assert.Equal(t, tc.success, run.Succeeded)
			assert.Equal(t, jobs[0].StartedAt, run.StartedAt)
			assert.GreaterOrEqual(t, run.Duration, time.Duration(0))
			if tc.success {
				assert.Equal(t, int64(len("archive")), run.SizeBytes)
				assert.Equal(t, jobs[0].StartedAt, storage.stored[0].StartedAt, "the archive is named after the recorded start")
			}
		})
	}
}

// TestVolumeService_SeedMetrics proves seeding forgets undeclared targets,
// restores the declared target's newest archive with its declared service,
// and ignores archives of targets that are no longer declared.
func TestVolumeService_SeedMetrics(t *testing.T) {
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"shop"}, nil).Once()
	expectShopVolume(state)
	started := time.Date(2026, 10, 17, 1, 30, 0, 0, time.UTC)
	storage := &fakeVolumeBackupStorage{jobs: []domain.VolumeBackupJob{
		{App: "shop", VolumeName: "data", StartedAt: started, SizeBytes: 42},
		{App: "gone", VolumeName: "old", StartedAt: started, SizeBytes: 1},
	}}
	metrics := outmocks.NewMockBackupMetrics(t)
	metrics.EXPECT().RetainBackupTargets(domain.BackupKindVolume, []domain.BackupTargetRef{shopData}, mock.Anything).Once()
	metrics.EXPECT().ObserveStoredBackup(domain.BackupRunRecord{
		BackupTargetRef: shopData, Succeeded: true, SizeBytes: 42, StartedAt: started,
	}).Once()
	svc := volumeTestService(t, state, &fakeVolumeArchiveExporter{}, storage, true).WithMetrics(metrics)

	require.NoError(t, svc.SeedMetrics(context.Background()))
}

func expectShopDatabase(state *outmocks.MockAppStateReader) {
	state.EXPECT().LoadIntent(mock.Anything, "shop").Return(domain.AppStopIntent{App: "shop"}, nil).Once()
	state.EXPECT().LoadActive(mock.Anything, "shop").Return(activeWith("shop", map[string]domain.AppService{
		"api": appSpecWithDatabase("api", "orders", "daily"),
	}, "ctr-api"), true, nil).Once()
}

// TestService_RecordsRunMetrics covers a failed and a successful dump. The
// recorded start is the timestamp the dump is stored under.
func TestService_RecordsRunMetrics(t *testing.T) {
	t.Run("pg_dump failure", func(t *testing.T) {
		state := outmocks.NewMockAppStateReader(t)
		expectShopDatabase(state)
		runtime, _, svc := backupTestService(t, state)
		metrics := outmocks.NewMockBackupMetrics(t)
		runs := captureRuns(metrics)
		svc.WithMetrics(metrics)
		runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.Anything).
			Return(&out.ExecResult{ExitCode: 1, Stderr: []byte("boom")}, nil).Twice()

		_, err := svc.RunBackup(context.Background(), "shop", "api", "orders")
		require.Error(t, err)
		require.Len(t, *runs, 1)
		assert.Equal(t, shopOrders, (*runs)[0].BackupTargetRef)
		assert.False(t, (*runs)[0].Succeeded)
		assert.False(t, (*runs)[0].StartedAt.IsZero())
	})

	t.Run("success", func(t *testing.T) {
		state := outmocks.NewMockAppStateReader(t)
		expectShopDatabase(state)
		runtime, storage, svc := backupTestService(t, state)
		metrics := outmocks.NewMockBackupMetrics(t)
		runs := captureRuns(metrics)
		svc.WithMetrics(metrics)
		runtime.EXPECT().ExecInContainer(mock.Anything, "ctr-api", mock.Anything).Return(&out.ExecResult{ExitCode: 0}, nil).Twice()
		runtime.EXPECT().CopyFromContainer(mock.Anything, "ctr-api", mock.Anything).
			Return(io.NopCloser(strings.NewReader("dump")), nil).Once()
		var storedAt time.Time
		storage.EXPECT().Store(mock.Anything, "shop", "api", "orders", domain.BackupSchedule(""), mock.Anything, mock.Anything).
			RunAndReturn(func(_ context.Context, _, _, _ string, _ domain.BackupSchedule, at time.Time, data io.Reader) (string, error) {
				storedAt = at
				_, err := io.Copy(io.Discard, data)
				return "/b/file.bak", err
			}).Once()

		_, err := svc.RunBackup(context.Background(), "shop", "api", "orders")
		require.NoError(t, err)
		require.Len(t, *runs, 1)
		assert.True(t, (*runs)[0].Succeeded)
		assert.Equal(t, int64(len("dump")), (*runs)[0].SizeBytes)
		assert.Equal(t, storedAt, (*runs)[0].StartedAt, "the dump is stored under the recorded start")
	})
}

// TestService_SeedMetrics proves database seeding forgets undeclared
// targets and only restores dumps of declared ones.
func TestService_SeedMetrics(t *testing.T) {
	state := outmocks.NewMockAppStateReader(t)
	state.EXPECT().ListApps(mock.Anything).Return([]string{"shop"}, nil).Twice()
	expectShopDatabase(state)
	started := time.Date(2026, 10, 17, 2, 0, 0, 0, time.UTC)

	_, storage, svc := backupTestService(t, state)
	metrics := outmocks.NewMockBackupMetrics(t)
	svc.WithMetrics(metrics)
	storage.EXPECT().List(mock.Anything, "shop", mock.Anything).Return([]domain.BackupJob{
		{App: "shop", Service: "api", DBName: "orders", StartedAt: started, SizeBytes: 7},
		{App: "shop", Service: "api", DBName: "dropped", StartedAt: started, SizeBytes: 1},
	}, nil).Once()
	metrics.EXPECT().RetainBackupTargets(domain.BackupKindDatabase, []domain.BackupTargetRef{shopOrders}, mock.Anything).Once()
	metrics.EXPECT().ObserveStoredBackup(domain.BackupRunRecord{
		BackupTargetRef: shopOrders, Succeeded: true, SizeBytes: 7, StartedAt: started,
	}).Once()

	require.NoError(t, svc.SeedMetrics(context.Background()))
}
