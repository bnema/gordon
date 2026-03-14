package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/domain"
	"github.com/spf13/cobra"
)

func newNetworksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "networks",
		Short: "Inspect Gordon-managed networks",
	}

	cmd.AddCommand(newNetworksListCmd())

	return cmd
}

func newNetworksListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List Gordon-managed Docker networks",
		Long: `Display Docker networks managed by Gordon, including which containers
are connected to each network.

Examples:
  gordon networks list
  gordon networks list --json
  gordon networks list --remote https://gordon.mydomain.com --token $TOKEN`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runNetworksList(ctx, handle.plane, cmd.OutOrStdout(), jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func runNetworksList(ctx context.Context, cp ControlPlane, out io.Writer, jsonOut bool) error {
	networks, err := cp.ListNetworks(ctx)
	if err != nil {
		return fmt.Errorf("failed to list networks: %w", err)
	}

	if jsonOut {
		return writeJSON(out, networks)
	}

	if len(networks) == 0 {
		return cliWriteLine(out, cliRenderMuted("No Gordon-managed networks found"))
	}

	if err := cliWriteLine(out, cliRenderTitle("Networks")); err != nil {
		return err
	}

	return renderNetworksTable(out, networks)
}

func renderNetworksTable(out io.Writer, networks []*domain.NetworkInfo) error {
	rows := make([][]string, 0, len(networks))
	for _, net := range networks {
		if net == nil {
			continue
		}
		containers := strings.Join(net.Containers, ", ")
		if containers == "" {
			containers = "-"
		}
		if len(containers) > 40 {
			containers = containers[:37] + "..."
		}
		rows = append(rows, []string{
			net.Name,
			net.Driver,
			fmt.Sprintf("%d", len(net.Containers)),
			containers,
		})
	}

	table := components.NewTable(
		components.WithColumns([]components.TableColumn{
			{Title: "Name", Width: 25},
			{Title: "Driver", Width: 10},
			{Title: "Containers", Width: 12},
			{Title: "Connected", Width: 40},
		}),
		components.WithRows(rows),
	)

	return cliWriteLine(out, table.View())
}
