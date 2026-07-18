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
func TestMigratePlanJSON(t *testing.T) {
	var out bytes.Buffer
	err := runMigrateOperation(context.Background(), migrationCLIFake{}, &out, true, func(ctx context.Context, s migrationControlPlane) (any, error) { return s.MigrationPlan(ctx) })
	require.NoError(t, err)
	assert.JSONEq(t, `{"checks":null,"ready":true}`, out.String())
}
