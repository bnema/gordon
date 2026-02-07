//go:build integration

package backup_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bnema/gordon/internal/adapters/out/docker"
	"github.com/bnema/gordon/internal/adapters/out/filesystem"
	"github.com/bnema/gordon/internal/domain"
	"github.com/bnema/gordon/internal/usecase/backup"
	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackupService_Integration_Postgres17And18(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	runtime := requireDockerRuntime(t)
	ctx := context.Background()

	versions := []string{"17", "18"}
	for _, version := range versions {
		t.Run("postgres_"+version, func(t *testing.T) {
			runPostgresBackupFlow(t, ctx, runtime, version)
		})
	}
}

func runPostgresBackupFlow(t *testing.T, ctx context.Context, runtime *docker.Runtime, version string) {
	image := fmt.Sprintf("postgres:%s", version)
	domainName := fmt.Sprintf("backup-it-%s.example.com", version)
	networkName := fmt.Sprintf("gordon-backup-it-%s-%d", version, time.Now().UnixNano())
	containerName := fmt.Sprintf("gordon-backup-it-%s-%d", version, time.Now().UnixNano())

	pullCtx, cancelPull := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelPull()
	require.NoError(t, runtime.PullImage(pullCtx, image))
	require.NoError(t, runtime.CreateNetwork(ctx, networkName, map[string]string{"driver": "bridge"}))
	t.Cleanup(func() { _ = runtime.RemoveNetwork(context.Background(), networkName) })

	containerCfg := &domain.ContainerConfig{
		Image:       image,
		Name:        containerName,
		Hostname:    "postgres",
		NetworkMode: networkName,
		Env: []string{
			"POSTGRES_USER=postgres",
			"POSTGRES_PASSWORD=postgres",
			"POSTGRES_DB=appdb",
		},
		Labels: map[string]string{
			domain.LabelManaged:    "true",
			domain.LabelAttachment: "true",
			domain.LabelAttachedTo: domainName,
			domain.LabelImage:      image,
		},
	}

	container, err := runtime.CreateContainer(ctx, containerCfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = runtime.StopContainer(context.Background(), container.ID)
		_ = runtime.RemoveContainer(context.Background(), container.ID, true)
	})
	require.NoError(t, runtime.StartContainer(ctx, container.ID))

	require.NoError(t, waitForPostgresReady(ctx, runtime, container.ID, 60*time.Second))
	require.NoError(t, seedPostgresData(ctx, runtime, container.ID))

	storage, err := filesystem.NewBackupStorage(t.TempDir(), zerowrap.Default())
	require.NoError(t, err)

	containerSvc := &integrationContainerService{
		routes: map[string]*domain.Container{
			domainName: container,
		},
		attachments: map[string][]domain.Attachment{
			domainName: {
				{
					Name:        "postgres",
					Image:       image,
					ContainerID: container.ID,
					Status:      container.Status,
					Network:     networkName,
					Ports:       []int{5432},
				},
			},
		},
	}

	svc := backup.NewService(runtime, storage, containerSvc, domain.BackupConfig{Enabled: true}, zerowrap.Default())

	detected, err := svc.DetectDatabases(ctx, domainName)
	require.NoError(t, err)
	require.Len(t, detected, 1)
	assert.Equal(t, domain.DBTypePostgreSQL, detected[0].Type)

	result, err := svc.RunBackup(ctx, domainName, "postgres")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, domain.BackupStatusCompleted, result.Job.Status)

	backupBytes, err := os.ReadFile(result.Job.FilePath)
	require.NoError(t, err)
	assert.Greater(t, len(backupBytes), 32)
	assert.True(t, bytes.HasPrefix(backupBytes, []byte("PGDMP")), "expected pg_dump custom format header")

	jobs, err := svc.ListBackups(ctx, domainName)
	require.NoError(t, err)
	assert.Len(t, jobs, 1)
}

func requireDockerRuntime(t *testing.T) *docker.Runtime {
	t.Helper()
	runtime, err := docker.NewRuntime()
	if err != nil {
		t.Skipf("docker runtime unavailable: %v", err)
	}
	if err := runtime.Ping(context.Background()); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}
	return runtime
}

func waitForPostgresReady(ctx context.Context, runtime *docker.Runtime, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		execCtx, cancelExec := context.WithTimeout(ctx, 10*time.Second)
		res, err := runtime.ExecInContainer(execCtx, containerID, []string{"pg_isready", "-U", "postgres", "-d", "appdb"})
		cancelExec()
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("postgres did not become ready within %s", timeout)
}

func seedPostgresData(ctx context.Context, runtime *docker.Runtime, containerID string) error {
	cmd := `psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d postgres -c "SELECT 1 FROM pg_database WHERE datname='${POSTGRES_DB}'" | grep -q 1 || createdb -U "$POSTGRES_USER" "$POSTGRES_DB"; psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -c "CREATE TABLE IF NOT EXISTS backup_items (id serial primary key, name text); INSERT INTO backup_items(name) VALUES ('one'), ('two');"`
	execCtx, cancelExec := context.WithTimeout(ctx, 30*time.Second)
	defer cancelExec()

	res, err := runtime.ExecInContainer(execCtx, containerID, []string{"sh", "-c", cmd})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("seed command failed: %s", strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

type integrationContainerService struct {
	routes      map[string]*domain.Container
	attachments map[string][]domain.Attachment
}

func (s *integrationContainerService) Deploy(context.Context, domain.Route) (*domain.Container, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *integrationContainerService) Stop(context.Context, string) error {
	return fmt.Errorf("not implemented")
}

func (s *integrationContainerService) Remove(context.Context, string, bool) error {
	return fmt.Errorf("not implemented")
}

func (s *integrationContainerService) Get(_ context.Context, domainName string) (*domain.Container, bool) {
	c, ok := s.routes[domainName]
	return c, ok
}

func (s *integrationContainerService) Restart(context.Context, string, bool) error {
	return fmt.Errorf("not implemented")
}

func (s *integrationContainerService) List(context.Context) map[string]*domain.Container {
	out := make(map[string]*domain.Container, len(s.routes))
	for k, v := range s.routes {
		out[k] = v
	}
	return out
}

func (s *integrationContainerService) ListRoutesWithDetails(context.Context) []domain.RouteInfo {
	return nil
}

func (s *integrationContainerService) ListAttachments(_ context.Context, domainName string) []domain.Attachment {
	attachments := s.attachments[domainName]
	out := make([]domain.Attachment, len(attachments))
	copy(out, attachments)
	return out
}

func (s *integrationContainerService) ListNetworks(context.Context) ([]*domain.NetworkInfo, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *integrationContainerService) HealthCheck(context.Context) map[string]bool {
	return map[string]bool{}
}

func (s *integrationContainerService) SyncContainers(context.Context) error {
	return nil
}

func (s *integrationContainerService) AutoStart(context.Context, []domain.Route) error {
	return nil
}

func (s *integrationContainerService) Shutdown(context.Context) error {
	return nil
}
