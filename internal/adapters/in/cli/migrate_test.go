package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/app"
)

type migrationCLIFake struct{}

func (migrationCLIFake) MigrationPlan(context.Context) (app.MigrationPreflightReport, error) {
	return app.MigrationPreflightReport{Ready: true}, nil
}
func (migrationCLIFake) MigrationPrepare(context.Context, app.MigrationCheckpoint) (*app.MigrationCheckpoint, error) {
	return nil, nil
}
func (migrationCLIFake) MigrationSwitch(context.Context) (*app.MigrationCheckpoint, error) {
	return nil, nil
}
func (migrationCLIFake) MigrationStatus(context.Context) (*app.MigrationCheckpoint, error) {
	return nil, nil
}
func (migrationCLIFake) MigrationResume(context.Context) (*app.MigrationCheckpoint, error) {
	return nil, nil
}
func (migrationCLIFake) MigrationCleanup(context.Context) error {
	return nil
}

type cleanupRecordingCLIFake struct {
	migrationCLIFake
	called bool
}

func (f *cleanupRecordingCLIFake) MigrationCleanup(context.Context) error {
	f.called = true
	return nil
}

func TestMigrateCleanupInvokesControlPlaneCleanup(t *testing.T) {
	originalResolver, originalConfigPath := resolveMigrationControlPlane, configPath
	t.Cleanup(func() { resolveMigrationControlPlane, configPath = originalResolver, originalConfigPath })
	fake := &cleanupRecordingCLIFake{}
	resolveMigrationControlPlane = func(string) (migrationControlPlane, func(), error) { return fake, func() {}, nil }
	configPath = ""

	var out bytes.Buffer
	cmd := newMigrateCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"cleanup"})
	require.NoError(t, cmd.ExecuteContext(context.Background()))
	assert.True(t, fake.called, "cleanup subcommand must invoke the control plane")
	assert.Contains(t, out.String(), "removed")
}
func TestLocalControlPlaneUsesKernelMigrationFacade(t *testing.T) {
	service, err := app.NewMigrationService(app.NewMigrationPreflight(app.MigrationPreflightProbes{}), mustMigrationStore(t), app.MigrationEnvOptions{Config: app.Config{}})
	require.NoError(t, err)
	plane := &localControlPlane{migration: func() (*app.MigrationService, error) { return service, nil }}
	report, err := plane.MigrationPlan(context.Background())
	require.NoError(t, err)
	assert.False(t, report.Ready)
}

func mustMigrationStore(t *testing.T) *app.MigrationCheckpointStore {
	t.Helper()
	store, err := app.NewMigrationCheckpointStore(t.TempDir() + "/migration.json")
	require.NoError(t, err)
	return store
}

func TestResolveMigrationControlPlaneReadsDurableStatusWithoutLocalKernel(t *testing.T) {
	resetControlPlaneResolutionTestState(t)

	originalNewLocalKernelQuiet := newLocalKernelQuiet
	newLocalKernelQuiet = func(string) (*app.Kernel, error) {
		t.Fatal("status resolver must not initialize a local monolith kernel")
		return nil, nil
	}
	t.Cleanup(func() { newLocalKernelQuiet = originalNewLocalKernelQuiet })

	service, closeFn, err := resolveMigrationControlPlane(writeCLIConfig(t, `[control]
listen_address = "0.0.0.0:9443"
endpoint = "https://component-grpc.example.test:9443"
`))
	require.NoError(t, err)
	defer closeFn()
	require.IsType(t, &durableMigrationControlPlane{}, service, "component gRPC endpoint is not an Admin HTTP target")
	checkpoint, err := service.MigrationStatus(context.Background())
	require.NoError(t, err)
	assert.Nil(t, checkpoint)
}

func TestResolveMigrationControlPlaneKeepsExplicitRemote(t *testing.T) {
	resetControlPlaneResolutionTestState(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/admin/migration/status", r.URL.Path)
		_, _ = w.Write([]byte(`{"phase":"switched"}`))
	}))
	defer server.Close()
	remoteFlag = server.URL

	originalNewLocalKernelQuiet := newLocalKernelQuiet
	newLocalKernelQuiet = func(string) (*app.Kernel, error) {
		return nil, errors.New("local checkpoint must not be opened for explicit remote")
	}
	t.Cleanup(func() { newLocalKernelQuiet = originalNewLocalKernelQuiet })

	service, closeFn, err := resolveMigrationControlPlane(writeCLIConfig(t, `[control]
listen_address = "0.0.0.0:9443"
endpoint = "https://component-grpc.example.test:9443"
`))
	require.NoError(t, err)
	defer closeFn()
	require.IsType(t, &remoteControlPlane{}, service)

	checkpoint, err := service.MigrationStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, app.MigrationPhaseSwitched, checkpoint.Phase)
}

func TestMigratePlanJSON(t *testing.T) {
	var out bytes.Buffer
	err := runMigrateOperation(context.Background(), migrationCLIFake{}, &out, true, func(ctx context.Context, s migrationControlPlane) (any, error) { return s.MigrationPlan(ctx) })
	require.NoError(t, err)
	assert.JSONEq(t, `{"checks":null,"ready":true}`, out.String())
}

func TestMigrateSubcommandsAcceptExplicitConfigBeforeAndAfterOperation(t *testing.T) {
	originalResolver, originalConfigPath := resolveMigrationControlPlane, configPath
	t.Cleanup(func() {
		resolveMigrationControlPlane, configPath = originalResolver, originalConfigPath
	})
	const explicitConfig = "/tmp/gordon-migration-fixture.toml"
	resolveMigrationControlPlane = func(path string) (migrationControlPlane, func(), error) {
		assert.Equal(t, explicitConfig, path)
		return migrationCLIFake{}, func() {}, nil
	}

	for _, operation := range []string{"plan", "prepare", "switch", "status", "resume", "cleanup"} {
		for _, args := range [][]string{{"--config", explicitConfig, operation}, {operation, "--config", explicitConfig}} {
			t.Run(operation+"/"+args[0], func(t *testing.T) {
				configPath = ""
				cmd := newMigrateCmd()
				cmd.SetArgs(args)
				require.NoError(t, cmd.ExecuteContext(context.Background()))
			})
		}
	}
}
