package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/app"
)

type deployer interface {
	Deploy(ctx context.Context, deployDomain string) (*remote.DeployResult, error)
}

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
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			return runDeploy(cmd.Context(), handle.plane, handle.isRemote, args[0])
		},
	}
}

func runDeploy(ctx context.Context, deployer deployer, isRemote bool, deployDomain string) error {
	result, err := deployer.Deploy(ctx, deployDomain)
	if err != nil {
		if isRemote && shouldFallbackToLocal(err) {
			domain, localErr := app.SendDeploySignal(deployDomain)
			if localErr == nil {
				fmt.Println(styles.RenderWarning(fmt.Sprintf("Remote deploy failed (%v), used local signal fallback", err)))
				fmt.Println(styles.RenderSuccess(fmt.Sprintf("Deploy signal sent for domain: %s", domain)))
				return nil
			}
			return fmt.Errorf("failed to deploy: remote error: %w; local fallback also failed: %v", err, localErr)
		}

		return formatDeployFailure(err)
	}

	containerID := result.ContainerID
	if len(containerID) > 12 {
		containerID = containerID[:12]
	}
	fmt.Println(styles.RenderSuccess(fmt.Sprintf("Deployed %s (container: %s)", deployDomain, containerID)))
	return nil
}
