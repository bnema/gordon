package backup

import (
	"context"
	"testing"

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
		return len(cmd) == 3 && cmd[0] == "sh" && cmd[1] == "-c"
	})).Return(&outiface.ExecResult{ExitCode: 0, Stdout: []byte("backup-data")}, nil)

	storage.EXPECT().Store(
		mock.Anything,
		"app.example.com",
		"postgres",
		domain.BackupSchedule(""),
		mock.Anything,
		mock.Anything,
	).Return("/tmp/backup.bak", nil)

	svc := NewService(runtime, storage, containerSvc, domain.BackupConfig{}, zerowrap.Default())

	result, err := svc.RunBackup(context.Background(), "app.example.com", "postgres")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, domain.BackupStatusCompleted, result.Job.Status)
	assert.Equal(t, "/tmp/backup.bak", result.Job.FilePath)
}
