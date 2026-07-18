package cli

import (
	"bytes"
	"context"
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
func TestLocalControlPlaneUsesKernelMigrationFacade(t *testing.T) {
	service, err := app.NewMigrationService(app.NewMigrationPreflight(app.MigrationPreflightProbes{}), mustMigrationStore(t))
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

	for _, operation := range []string{"plan", "prepare", "switch", "status", "resume"} {
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
