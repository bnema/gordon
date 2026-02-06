package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/app"
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
  gordon deploy api.example.com
  gordon deploy myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			deployDomain := args[0]

			client, isRemote := GetRemoteClient()
			if isRemote {
				// Remote deployment via admin API
				result, err := client.Deploy(ctx, deployDomain)
				if err != nil {
					if shouldFallbackToLocal(err) {
						domain, localErr := app.SendDeploySignal(deployDomain)
						if localErr == nil {
							fmt.Println(styles.RenderWarning(fmt.Sprintf("Remote deploy failed (%v), used local signal fallback", err)))
							fmt.Println(styles.RenderSuccess(fmt.Sprintf("Deploy signal sent for domain: %s", domain)))
							return nil
						}
					}
					return fmt.Errorf("failed to deploy: %w", err)
				}
				containerID := result.ContainerID
				if len(containerID) > 12 {
					containerID = containerID[:12]
				}
				fmt.Println(styles.RenderSuccess(fmt.Sprintf("Deployed %s (container: %s)", deployDomain, containerID)))
				return nil
			}

			// Local deployment via signal
			domain, err := app.SendDeploySignal(deployDomain)
			if err != nil {
				return err
			}
			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Deploy signal sent for domain: %s", domain)))
			return nil
		},
	}
}
