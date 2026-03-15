package cli

import (
	"fmt"

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
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			if err := handle.plane.AddAutoRouteAllowedDomain(cmd.Context(), args[0]); err != nil {
				return fmt.Errorf("failed to add auto-route allowed domain: %w", err)
			}
			return cliWriteLine(cmd.OutOrStdout(), cliRenderSuccess("Allowed domain added"))
		},
	}
}

func newAutorouteAllowListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List auto-route allowed domains",
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			domains, err := handle.plane.GetAutoRouteAllowedDomains(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to list auto-route allowed domains: %w", err)
			}
			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), map[string]any{"domains": domains})
			}
			if len(domains) == 0 {
				return cliWriteLine(cmd.OutOrStdout(), cliRenderMuted("No allowed domains configured"))
			}
			for _, domain := range domains {
				if err := cliWriteLine(cmd.OutOrStdout(), domain); err != nil {
					return err
				}
			}
			return nil
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
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			if err := handle.plane.RemoveAutoRouteAllowedDomain(cmd.Context(), args[0]); err != nil {
				return fmt.Errorf("failed to remove auto-route allowed domain: %w", err)
			}
			return cliWriteLine(cmd.OutOrStdout(), cliRenderSuccess("Allowed domain removed"))
		},
	}
}
