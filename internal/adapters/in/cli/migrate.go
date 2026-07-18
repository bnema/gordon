package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/app"
)

// migrationControlPlane is intentionally separate from the legacy management
// interface so adding migration does not silently grant runtime access to any
// existing CLI operation.
type migrationControlPlane interface {
	MigrationPlan(context.Context) (app.MigrationPreflightReport, error)
	MigrationPrepare(context.Context, app.MigrationCheckpoint) (*app.MigrationCheckpoint, error)
	MigrationSwitch(context.Context) (*app.MigrationCheckpoint, error)
	MigrationStatus(context.Context) (*app.MigrationCheckpoint, error)
	MigrationResume(context.Context) (*app.MigrationCheckpoint, error)
}

func newMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "migrate", Short: "Plan and safely resume Gordon component migration"}
	cmd.AddCommand(newMigratePlanCmd(), newMigratePrepareCmd(), newMigrateSwitchCmd(), newMigrateStatusCmd(), newMigrateResumeCmd())
	return cmd
}

func newMigratePlanCmd() *cobra.Command {
	return newMigrateOperationCmd("plan", func(ctx context.Context, service migrationControlPlane) (any, error) {
		return service.MigrationPlan(ctx)
	})
}
func newMigratePrepareCmd() *cobra.Command {
	return newMigrateOperationCmd("prepare", func(ctx context.Context, service migrationControlPlane) (any, error) {
		return service.MigrationPrepare(ctx, app.MigrationCheckpoint{})
	})
}
func newMigrateSwitchCmd() *cobra.Command {
	return newMigrateOperationCmd("switch", func(ctx context.Context, service migrationControlPlane) (any, error) {
		return service.MigrationSwitch(ctx)
	})
}
func newMigrateStatusCmd() *cobra.Command {
	return newMigrateOperationCmd("status", func(ctx context.Context, service migrationControlPlane) (any, error) {
		return service.MigrationStatus(ctx)
	})
}
func newMigrateResumeCmd() *cobra.Command {
	return newMigrateOperationCmd("resume", func(ctx context.Context, service migrationControlPlane) (any, error) {
		return service.MigrationResume(ctx)
	})
}

func newMigrateOperationCmd(name string, operation func(context.Context, migrationControlPlane) (any, error)) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{Use: name, RunE: func(cmd *cobra.Command, _ []string) error {
		handle, err := resolveControlPlane(configPath)
		if err != nil {
			return err
		}
		defer handle.close()
		service, ok := handle.plane.(migrationControlPlane)
		if !ok {
			return fmt.Errorf("migration is unavailable from this control plane")
		}
		return runMigrateOperation(cmd.Context(), service, cmd.OutOrStdout(), jsonOut, operation)
	}}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runMigrateOperation(ctx context.Context, service migrationControlPlane, out io.Writer, jsonOut bool, operation func(context.Context, migrationControlPlane) (any, error)) error {
	result, err := operation(ctx, service)
	if err != nil {
		return err
	}
	if jsonOut {
		return writeJSON(out, result)
	}
	return cliWritef(out, "%v\n", result)
}
