package cli

import (
	"context"

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
