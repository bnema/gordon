package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/dto"
	climocks "github.com/bnema/gordon/internal/adapters/in/cli/mocks"
)

func withBackupControlPlane(t *testing.T, plane ControlPlane) {
	t.Helper()
	old := backupResolveControlPlane
	backupResolveControlPlane = func(context.Context, string, string) (*controlPlaneHandle, error) {
		return &controlPlaneHandle{plane: plane}, nil
	}
	t.Cleanup(func() { backupResolveControlPlane = old })
}

func TestVolumeBackupRun_UsesDomainAwareResolver(t *testing.T) {
	plane := climocks.NewMockControlPlane(t)
	old := backupResolveControlPlane
	var gotDomain string
	backupResolveControlPlane = func(_ context.Context, _ string, domainName string) (*controlPlaneHandle, error) {
		gotDomain = domainName
		return &controlPlaneHandle{plane: plane}, nil
	}
	t.Cleanup(func() { backupResolveControlPlane = old })

	plane.EXPECT().RunVolumeBackups(mock.Anything, "app.example.com", "").Return(&dto.VolumeBackupRunResponse{Status: "ok"}, nil)

	cmd := newVolumeBackupRunCmd()
	cmd.SetArgs([]string{"app.example.com"})

	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.Equal(t, "app.example.com", gotDomain)
}

func TestVolumeBackupRun_JSONFlag(t *testing.T) {
	plane := climocks.NewMockControlPlane(t)
	withBackupControlPlane(t, plane)

	plane.EXPECT().RunVolumeBackups(mock.Anything, "app.example.com", "gordon-app-data").Return(&dto.VolumeBackupRunResponse{
		Status:  "ok",
		Backups: []dto.VolumeBackupJob{{Domain: "app.example.com", VolumeName: "gordon-app-data", Status: "completed"}},
	}, nil)

	cmd := newVolumeBackupRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"app.example.com", "--volume", "gordon-app-data", "--json"})

	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.JSONEq(t, `[{"id":"","domain":"app.example.com","volume_name":"gordon-app-data","type":"","status":"completed","size_bytes":0}]`, out.String())
}

func TestVolumeBackupStatus_JSONFlag(t *testing.T) {
	plane := climocks.NewMockControlPlane(t)
	withBackupControlPlane(t, plane)

	plane.EXPECT().VolumeBackupStatus(mock.Anything).Return([]dto.VolumeBackupJob{{Domain: "app.example.com", VolumeName: "gordon-app-data", Status: "running"}}, nil)

	cmd := newVolumeBackupStatusCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})

	require.NoError(t, cmd.ExecuteContext(context.Background()))
	require.JSONEq(t, `[{"id":"","domain":"app.example.com","volume_name":"gordon-app-data","type":"","status":"running","size_bytes":0}]`, out.String())
}

func TestVolumeBackupRun_PrintsPartialJobsBeforeReturningError(t *testing.T) {
	plane := climocks.NewMockControlPlane(t)
	withBackupControlPlane(t, plane)

	runErr := errors.New("one backup failed")
	plane.EXPECT().RunVolumeBackups(mock.Anything, "app.example.com", "").Return(&dto.VolumeBackupRunResponse{
		Status:  "partial",
		Backups: []dto.VolumeBackupJob{{Domain: "app.example.com", VolumeName: "gordon-app-data", Status: "completed"}},
		Error:   runErr.Error(),
	}, runErr)

	cmd := newVolumeBackupRunCmd()
	cmd.SilenceUsage = true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"app.example.com", "--json"})

	err := cmd.ExecuteContext(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, runErr)
	require.JSONEq(t, `[{"id":"","domain":"app.example.com","volume_name":"gordon-app-data","type":"","status":"completed","size_bytes":0}]`, out.String())
}
