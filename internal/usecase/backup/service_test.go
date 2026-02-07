package backup

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	inmocks "github.com/bnema/gordon/internal/boundaries/in/mocks"
	outiface "github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestService_DetectDatabases_PostgresAttachment(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	storage := outmocks.NewMockBackupStorage(t)
	containerSvc := inmocks.NewMockContainerService(t)

	containerSvc.EXPECT().ListAttachments(mock.Anything, "app.example.com").Return([]domain.Attachment{
		{Name: "postgres", Image: "postgres:17", ContainerID: "db1", Status: "running"},
	})

	svc := NewService(runtime, storage, containerSvc, domain.BackupConfig{}, zerowrap.Default())

	dbs, err := svc.DetectDatabases(context.Background(), "app.example.com")
	require.NoError(t, err)
	require.Len(t, dbs, 1)
	assert.Equal(t, domain.DBTypePostgreSQL, dbs[0].Type)
	assert.Equal(t, "db1", dbs[0].ContainerID)
}

func TestService_RunBackup_Postgres(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	storage := outmocks.NewMockBackupStorage(t)
	containerSvc := inmocks.NewMockContainerService(t)

	containerSvc.EXPECT().ListAttachments(mock.Anything, "app.example.com").Return([]domain.Attachment{
		{Name: "postgres", Image: "postgres:17", ContainerID: "db123", Status: "running"},
	})

	runtime.EXPECT().ExecInContainer(mock.Anything, "db123", mock.MatchedBy(func(cmd []string) bool {
		if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
			return false
		}
		return bytes.Contains([]byte(cmd[2]), []byte("pg_dump -Fc")) &&
			bytes.Contains([]byte(cmd[2]), []byte("PGDATABASE='postgres'")) &&
			bytes.Contains([]byte(cmd[2]), []byte(" > "))
	})).Return(&outiface.ExecResult{ExitCode: 0, Stdout: []byte("backup-data")}, nil)

	runtime.EXPECT().CopyFromContainer(mock.Anything, "db123", mock.MatchedBy(func(path string) bool {
		return path != ""
	})).Return(io.NopCloser(bytes.NewReader([]byte("backup-data"))), nil)

	runtime.EXPECT().ExecInContainer(mock.Anything, "db123", mock.MatchedBy(func(cmd []string) bool {
		return len(cmd) == 3 && cmd[0] == "sh" && cmd[1] == "-c" && bytes.Contains([]byte(cmd[2]), []byte("rm -f"))
	})).Return(&outiface.ExecResult{ExitCode: 0}, nil)

	storage.EXPECT().Store(
		mock.Anything,
		"app.example.com",
		"postgres",
		domain.BackupSchedule(""),
		mock.Anything,
		mock.MatchedBy(func(r io.Reader) bool {
			data, _ := io.ReadAll(r)
			return string(data) == "backup-data"
		}),
	).Return("/tmp/backup.bak", nil)

	svc := NewService(runtime, storage, containerSvc, domain.BackupConfig{}, zerowrap.Default())

	result, err := svc.RunBackup(context.Background(), "app.example.com", "postgres")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, domain.BackupStatusCompleted, result.Job.Status)
	assert.Equal(t, "/tmp/backup.bak", result.Job.FilePath)
	assert.Equal(t, int64(len("backup-data")), result.Job.SizeBytes)
}

func TestSelectDatabaseRequiresExplicitNameWhenMultipleDetected(t *testing.T) {
	db, err := selectDatabase([]domain.DBInfo{
		{Name: "postgres"},
		{Name: "analytics"},
	}, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple database attachments detected")
	assert.Equal(t, domain.DBInfo{}, db)
}

func TestSelectDatabaseAutoSelectsOnlyDatabaseWhenUnspecified(t *testing.T) {
	db, err := selectDatabase([]domain.DBInfo{{Name: "postgres"}}, "")
	require.NoError(t, err)
	assert.Equal(t, "postgres", db.Name)
}

func TestPostgresDumpCommandQuotesDatabaseName(t *testing.T) {
	cmd := postgresDumpCommand("customer data")
	assert.Contains(t, cmd, "PGDATABASE='customer data'")
}

func TestServiceStatusReturnsWhenContextCancelledDuringSemaphoreAcquire(t *testing.T) {
	runtime := outmocks.NewMockContainerRuntime(t)
	storage := outmocks.NewMockBackupStorage(t)
	containerSvc := inmocks.NewMockContainerService(t)

	containerSvc.EXPECT().List(mock.Anything).Return(map[string]*domain.Container{
		"a.example.com": {},
		"b.example.com": {},
		"c.example.com": {},
		"d.example.com": {},
		"e.example.com": {},
	})

	unblock := make(chan struct{})
	defer close(unblock)

	var started int32
	startedFour := make(chan struct{})
	storage.EXPECT().List(mock.Anything, mock.Anything, (*domain.BackupSchedule)(nil)).RunAndReturn(
		func(context.Context, string, *domain.BackupSchedule) ([]domain.BackupJob, error) {
			if atomic.AddInt32(&started, 1) == 4 {
				close(startedFour)
			}
			<-unblock
			return nil, nil
		},
	).Times(4)

	svc := NewService(runtime, storage, containerSvc, domain.BackupConfig{}, zerowrap.Default())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := svc.Status(ctx)
		errCh <- err
	}()

	<-startedFour
	cancel()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Status did not return after context cancellation while waiting for semaphore")
	}
}
