package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/bnema/gordon/internal/app"

	"github.com/spf13/cobra"
)

// newServeCmd creates the serve command.
func newServeCmd() *cobra.Command {
	var configPath string
	var component string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Gordon server",
		Long: `Start the Gordon server.

In v3, Gordon runs as multiple isolated containers. Use --component to specify which component to run:
  --component=core     - Orchestrator with Docker socket and admin API
  --component=proxy    - HTTP reverse proxy (internet-facing)
  --component=registry - Docker registry with gRPC inspection
  --component=secrets  - Secrets and token management

If no component is specified, runs in backward-compatible monolithic mode (deprecated).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			switch component {
			case "":
				// Backward compatibility: run monolithic mode
				return app.Run(ctx, configPath)
			case "core":
				return app.RunCore(ctx, configPath)
			case "proxy":
				return app.RunProxy(ctx, configPath)
			case "registry":
				return app.RunRegistry(ctx, configPath)
			case "secrets":
				return app.RunSecrets(ctx, configPath)
			default:
				return fmt.Errorf("unknown component: %s (valid: core, proxy, registry, secrets)", component)
			}
		},
	}

	// Add flags
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().StringVar(&component, "component", "", "Component to run (core|proxy|registry|secrets)")

	return cmd
}

// newStartCmd creates a deprecated alias for serve.
func newStartCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:        "start",
		Short:      "Start the Gordon server (deprecated: use 'serve')",
		Long:       `Start the Gordon server. This command is deprecated, please use 'gordon serve' instead.`,
		Deprecated: "use 'gordon serve' instead",
		Hidden:     true, // Hide from help but still works
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(os.Stderr, "\nWarning: 'gordon start' is deprecated, use 'gordon serve' instead")
			return app.Run(context.Background(), configPath)
		},
	}

	// Add flags (same as serve)
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")

	return cmd
}
