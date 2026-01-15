package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"gordon/internal/app"
)

// newDeployCmd creates the deploy command.
func newDeployCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deploy <domain>",
		Short: "Manually deploy or redeploy a route",
		Long: `Triggers a deployment for the specified route domain.
The route must be configured in config.toml.

This will pull the latest image and deploy/redeploy the container,
even if a container is already running.

Examples:
  gordon deploy myapp.example.com
  gordon deploy api.example.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			domain, err := app.SendDeploySignal(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("Deploy signal sent for domain: %s\n", domain)
			return nil
		},
	}
}
