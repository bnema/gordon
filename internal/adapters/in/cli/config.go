package cli

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect Gordon configuration",
	}

	cmd.AddCommand(newConfigShowCmd())

	return cmd
}

func newConfigShowCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show server configuration",
		Long: `Display the full Gordon server configuration including server settings,
auto-route, network isolation, routes, and external routes.

Examples:
  gordon config show
  gordon config show --json
  gordon config show --remote https://gordon.mydomain.com --token $TOKEN`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := context.Background()
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runConfigShow(ctx, handle.plane, cmd.OutOrStdout(), jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func runConfigShow(ctx context.Context, cp ControlPlane, out io.Writer, jsonOut bool) error {
	config, err := cp.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get config: %w", err)
	}

	if jsonOut {
		return writeJSON(out, config)
	}

	return renderConfigTable(out, config)
}

func renderConfigTable(out io.Writer, config *remote.Config) error {
	if err := renderConfigSummary(out, config); err != nil {
		return err
	}
	if err := renderConfigRoutes(out, config); err != nil {
		return err
	}
	return renderConfigExternalRoutes(out, config)
}

func renderConfigSummary(out io.Writer, config *remote.Config) error {
	if err := cliWriteLine(out, cliRenderTitle("Gordon Configuration")); err != nil {
		return err
	}
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Server Port:", fmt.Sprintf("%d", config.Server.Port))); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Registry Port:", fmt.Sprintf("%d", config.Server.RegistryPort))); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Registry Domain:", config.Server.RegistryDomain)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Data Directory:", config.Server.DataDir)); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Auto-Route:", fmt.Sprintf("%v", config.AutoRoute.Enabled))); err != nil {
		return err
	}
	if err := cliWriteLine(out, cliRenderMeta("Network Isolation:", fmt.Sprintf("%v", config.NetworkIsolation.Enabled))); err != nil {
		return err
	}
	if config.NetworkIsolation.Prefix != "" {
		if err := cliWriteLine(out, cliRenderMeta("Network Prefix:", config.NetworkIsolation.Prefix)); err != nil {
			return err
		}
	}
	return nil
}

func renderConfigRoutes(out io.Writer, config *remote.Config) error {
	if err := cliWriteLine(out, ""); err != nil {
		return err
	}
	if len(config.Routes) == 0 {
		if err := cliWriteLine(out, cliRenderMuted("No routes configured")); err != nil {
			return err
		}
	} else {
		if err := cliWriteLine(out, cliRenderTitle("Routes")); err != nil {
			return err
		}
		rows := make([][]string, 0, len(config.Routes))
		for _, route := range config.Routes {
			rows = append(rows, []string{route.Domain, route.Image})
		}
		table := components.NewTable(
			components.WithColumns([]components.TableColumn{
				{Title: "Domain", Width: 30},
				{Title: "Image", Width: 45},
			}),
			components.WithRows(rows),
		)
		if err := cliWriteLine(out, table.View()); err != nil {
			return err
		}
	}
	return nil
}

func renderConfigExternalRoutes(out io.Writer, config *remote.Config) error {
	if len(config.ExternalRoutes) > 0 {
		if err := cliWriteLine(out, ""); err != nil {
			return err
		}
		if err := cliWriteLine(out, cliRenderTitle("External Routes")); err != nil {
			return err
		}
		domains := make([]string, 0, len(config.ExternalRoutes))
		for d := range config.ExternalRoutes {
			domains = append(domains, d)
		}
		sort.Strings(domains)

		rows := make([][]string, 0, len(domains))
		for _, d := range domains {
			rows = append(rows, []string{d, config.ExternalRoutes[d]})
		}
		table := components.NewTable(
			components.WithColumns([]components.TableColumn{
				{Title: "Domain", Width: 30},
				{Title: "Target", Width: 45},
			}),
			components.WithRows(rows),
		)
		if err := cliWriteLine(out, table.View()); err != nil {
			return err
		}
	}

	return nil
}
