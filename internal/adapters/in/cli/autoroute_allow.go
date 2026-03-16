package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func newAutorouteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "autoroute",
		Short: "Manage auto-route settings",
	}
	cmd.AddCommand(newAutorouteAllowCmd())
	return cmd
}

func newAutorouteAllowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "allow",
		Short: "Manage auto-route allowed domains",
	}
	cmd.AddCommand(newAutorouteAllowAddCmd())
	cmd.AddCommand(newAutorouteAllowListCmd())
	cmd.AddCommand(newAutorouteAllowRemoveCmd())
	return cmd
}

func newAutorouteAllowAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <pattern>",
		Short: "Allow an auto-route domain pattern",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAutorouteAllowAdd(cmd.Context(), args, cmd.OutOrStdout())
		},
	}
}

func newAutorouteAllowListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List auto-route allowed domains",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAutorouteAllowList(cmd.Context(), cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newAutorouteAllowRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <pattern>",
		Short: "Remove an auto-route domain pattern",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAutorouteAllowRemove(cmd.Context(), args, cmd.OutOrStdout())
		},
	}
}

func runAutorouteAllowAdd(ctx context.Context, args []string, out io.Writer) error {
	handle, err := resolveControlPlane(configPath)
	if err != nil {
		return err
	}
	defer handle.close()
	if err := handle.plane.AddAutoRouteAllowedDomain(ctx, args[0]); err != nil {
		return fmt.Errorf("failed to add auto-route allowed domain: %w", err)
	}
	return cliWriteLine(out, cliRenderSuccess("Allowed domain added"))
}

func runAutorouteAllowList(ctx context.Context, out io.Writer, jsonOut bool) error {
	handle, err := resolveControlPlane(configPath)
	if err != nil {
		return err
	}
	defer handle.close()
	domains, err := handle.plane.GetAutoRouteAllowedDomains(ctx)
	if err != nil {
		return fmt.Errorf("failed to list auto-route allowed domains: %w", err)
	}
	if jsonOut {
		return writeJSON(out, map[string]any{"domains": domains})
	}
	if len(domains) == 0 {
		return cliWriteLine(out, cliRenderMuted("No allowed domains configured"))
	}
	for _, domain := range domains {
		if err := cliWriteLine(out, domain); err != nil {
			return err
		}
	}
	return nil
}

func runAutorouteAllowRemove(ctx context.Context, args []string, out io.Writer) error {
	handle, err := resolveControlPlane(configPath)
	if err != nil {
		return err
	}
	defer handle.close()
	if err := handle.plane.RemoveAutoRouteAllowedDomain(ctx, args[0]); err != nil {
		return fmt.Errorf("failed to remove auto-route allowed domain: %w", err)
	}
	return cliWriteLine(out, cliRenderSuccess("Allowed domain removed"))
}
