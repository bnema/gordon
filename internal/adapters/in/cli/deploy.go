package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/app"
)

type deployer interface {
	Deploy(ctx context.Context, deployDomain string) (*remote.DeployResult, error)
}

var sendDeploySignal = app.SendDeploySignal

// newDeployCmd creates the deploy command.
func newDeployCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
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

			return runDeploy(cmd.Context(), handle.plane, handle.isRemote, args[0], cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runDeploy(ctx context.Context, deployer deployer, isRemote bool, deployDomain string, out io.Writer, jsonOut bool) error {
	result, err := deployer.Deploy(ctx, deployDomain)
	if err != nil {
		if formatted, ok := structuredDeployFailure(err); ok {
			return formatted
		}

		if isRemote && shouldFallbackToLocal(err) {
			return handleLocalDeployFallback(err, deployDomain, out, jsonOut)
		}

		return formatDeployFailure(err)
	}

	if jsonOut {
		return writeJSON(out, result)
	}

	messageDomain := deployDomain
	if result.Domain != "" {
		messageDomain = result.Domain
	}

	containerID := result.ContainerID
	if containerID != "" && len(containerID) > 12 {
		containerID = containerID[:12]
	}

	switch result.Status {
	case "deployed":
		msg := fmt.Sprintf("Deployed %s", messageDomain)
		if containerID != "" {
			msg += fmt.Sprintf(" (container: %s)", containerID)
		}
		return cliWriteLine(out, cliRenderSuccess(msg))
	case "queued":
		return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Deploy queued for domain: %s", messageDomain)))
	default:
		msg := fmt.Sprintf("Deploy status for %s: %s", messageDomain, result.Status)
		if containerID != "" {
			msg += fmt.Sprintf(" (container: %s)", containerID)
		}
		return cliWriteLine(out, cliRenderInfo(msg))
	}
}

func handleLocalDeployFallback(err error, deployDomain string, out io.Writer, jsonOut bool) error {
	domain, localErr := sendDeploySignal(deployDomain)
	if localErr != nil {
		return fmt.Errorf("failed to deploy: remote error: %w; local fallback also failed: %v", err, localErr)
	}

	warning := fmt.Sprintf("Remote deploy failed (%v), used local signal fallback", err)
	if jsonOut {
		return writeJSON(out, map[string]string{
			"warning": warning,
			"domain":  domain,
			"status":  "success",
		})
	}

	if writeErr := cliWriteLine(out, cliRenderWarning(warning)); writeErr != nil {
		return writeErr
	}

	return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Deploy signal sent for domain: %s", domain)))
}
