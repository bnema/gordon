package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newAppsOperationsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "operations", Short: "Inspect app operation journals"}
	cmd.AddCommand(newAppsOperationsShowCmd(), newAppsOperationsWatchCmd())
	return cmd
}

func newAppsOperationsShowCmd() *cobra.Command {
	var key string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show APP",
		Short: "Show an app operation by request key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveAppPlane()
			if err != nil {
				return err
			}
			defer handle.close()
			return runAppsOperationsShow(cmd.Context(), handle.plane, cmd.OutOrStdout(), args[0], key, jsonOut)
		},
	}
	cmd.Flags().StringVar(&key, "key", "", "Request key (required)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("key")
	return cmd
}

func runAppsOperationsShow(ctx context.Context, plane ControlPlane, out io.Writer, app, key string, jsonOut bool) error {
	if app == "" || key == "" {
		return fmt.Errorf("APP and --key are required")
	}
	operation, err := plane.OperationByKey(ctx, app, key)
	if err != nil {
		return fmt.Errorf("lookup operation for app %s: %w", app, err)
	}
	if jsonOut {
		return writeJSON(out, operation)
	}
	return renderAppDeployResponse(out, operation)
}
