package cli

import (
	"context"
	"os"

	"github.com/bnema/gordon/internal/app"

	"github.com/spf13/cobra"
)

// newServeCmd creates the serve command.
func newServeCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Gordon server",
		Long:  `Start the Gordon server, including the registry and proxy components.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Run(context.Background(), configPath)
		},
	}

	// Add flags
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")

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
			if err := cliWriteLine(os.Stderr, cliRenderWarning("Warning: 'gordon start' is deprecated, use 'gordon serve' instead")); err != nil {
				return err
			}
			return app.Run(context.Background(), configPath)
		},
	}

	// Add flags (same as serve)
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")

	return cmd
}
