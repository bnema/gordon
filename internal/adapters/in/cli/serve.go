package cli

import (
	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/app"
)

// newServeCmd creates the serve command.
func newServeCmd() *cobra.Command {
	var configPath string
	var role string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the Gordon server",
		Long:  `Start the Gordon server, including the registry and proxy components.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.RunWithRole(cmd.Context(), configPath, role)
		},
	}

	// Add flags
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().StringVar(&role, "role", "", "Gordon role to run (monolith, control, runtime, edge, registry)")

	return cmd
}
