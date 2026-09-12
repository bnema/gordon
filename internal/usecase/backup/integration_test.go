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

	"github.com/bnema/gordon/internal/adapters/out/appstate"
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
	appName := fmt.Sprintf("backup-it-%s", version)
	networkName := fmt.Sprintf("gordon-backup-it-%s-%d", version, time.Now().UnixNano())
	containerName := fmt.Sprintf("gordon-backup-it-%s-%d", version, time.Now().UnixNano())

	pullCtx, cancelPull := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelPull()
	require.NoError(t, runtime.PullImage(pullCtx, image))
	netCtx, cancelNet := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelNet()
	require.NoError(t, runtime.CreateNetwork(netCtx, networkName, domain.NetworkConfig{Driver: "bridge"}))
	t.Cleanup(func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelCleanup()
		_ = runtime.RemoveNetwork(cleanupCtx, networkName)
	})

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
			domain.LabelManaged: "true",
			domain.LabelApp:     appName,
			domain.LabelService: "postgres",
			domain.LabelImage:   image,
		},
	}

	contCtx, cancelCont := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelCont()
	container, err := runtime.CreateContainer(contCtx, containerCfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		stopCtx, cancelStop := context.WithTimeout(context.Background(), 20*time.Second)
		_ = runtime.StopContainer(stopCtx, container.ID, domain.AppDefaultStopGrace)
		cancelStop()

		removeCtx, cancelRemove := context.WithTimeout(context.Background(), 20*time.Second)
		_ = runtime.RemoveContainer(removeCtx, container.ID, true)
		cancelRemove()
	})
	startCtx, cancelStart := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelStart()
	require.NoError(t, runtime.StartContainer(startCtx, container.ID))

	require.NoError(t, waitForPostgresReady(ctx, runtime, container.ID, 60*time.Second))
	require.NoError(t, seedPostgresData(ctx, runtime, container.ID))

	storage, err := filesystem.NewBackupStorage(t.TempDir(), zerowrap.Default())
	require.NoError(t, err)

	state, err := appstate.NewStore(t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, state.Close()) })
	require.NoError(t, state.SaveActive(ctx, domain.AppActive{
		App:       appName,
		Converged: true,
		Services: map[string]domain.AppEffectiveService{
			"postgres": {
				Image:     image,
				Container: container.ID,
				Spec: domain.AppService{
					Name:      "postgres",
					Image:     image,
					Databases: []domain.AppDatabase{{Name: "appdb", Type: domain.AppDBPostgres, Schedule: "daily"}},
					Backup:    domain.AppBackup{Postgres: []string{"appdb"}},
				},
			},
		},
	}))

	svc := backup.NewService(runtime, storage, domain.BackupConfig{Enabled: true}, zerowrap.Default()).WithAppState(state)
	targets, err := svc.Targets(ctx, appName)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "postgres", targets[0].Service)

	result, err := svc.RunBackup(ctx, appName, "postgres", "appdb")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, domain.BackupStatusCompleted, result.Job.Status)

	backupBytes, err := os.ReadFile(result.Job.FilePath)
	require.NoError(t, err)
	assert.Greater(t, len(backupBytes), 32)
	assert.True(t, bytes.HasPrefix(backupBytes, []byte("PGDMP")), "expected pg_dump custom format header")

	jobs, err := svc.ListBackups(ctx, appName)
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
