package cli

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
)

// newStatusCmd creates the status command.
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Gordon server status",
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(cliConfigPath)
			if err != nil {
				return err
			}
			defer handle.close()

			return runStatusCmd(cmd.Context(), handle.plane, cmd.OutOrStdout())
		},
	}
}

func runStatusCmd(ctx context.Context, plane ControlPlane, out io.Writer) error {
	status, err := plane.GetStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}
	if status == nil {
		return fmt.Errorf("failed to get status: empty response")
	}

	if err := cliWriteLine(out, styles.Theme.Title.Render("Gordon Status")); err != nil {
		return err
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}

	if err := cliWriteLine(out, fmt.Sprintf("%s %s", styles.Theme.Bold.Render("Domain:"), status.RegistryDomain)); err != nil {
		return err
	}
	if err := cliWriteLine(out, fmt.Sprintf("%s %d", styles.Theme.Bold.Render("Registry Port:"), status.RegistryPort)); err != nil {
		return err
	}
	if err := cliWriteLine(out, fmt.Sprintf("%s %d", styles.Theme.Bold.Render("Server Port:"), status.ServerPort)); err != nil {
		return err
	}
	if err := cliWriteLine(out, fmt.Sprintf("%s %d", styles.Theme.Bold.Render("Apps:"), status.Apps)); err != nil {
		return err
	}
	if err := cliWriteLine(out, fmt.Sprintf("%s %v", styles.Theme.Bold.Render("Network Isolation:"), status.NetworkIsolation)); err != nil {
		return err
	}

	return renderStatusFleet(out, status.ContainerStatus)
}

func renderStatusFleet(out io.Writer, fleet map[string]string) error {
	if len(fleet) == 0 {
		return nil
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, styles.Theme.Bold.Render("Container Status:")); err != nil {
		return err
	}
	names := make([]string, 0, len(fleet))
	for name := range fleet {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		badge := components.ContainerStatusBadge(fleet[name])
		if err := cliWriteLine(out, fmt.Sprintf("  %s: %s", name, badge)); err != nil {
			return err
		}
	}
	return nil
}
