package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/app"
)

func newRestartCmd() *cobra.Command {
	var withAttachments bool

	cmd := &cobra.Command{
		Use:   "restart <domain>",
		Short: "Restart a running container",
		Long: `Restarts the container for the specified route domain.
Useful after changing environment variables with 'gordon secrets set'.

Use --with-attachments to also restart attached services (databases, caches).

Examples:
  gordon restart myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN
  gordon restart myapp.example.com --with-attachments --remote ...`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			restartDomain := args[0]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				if withAttachments {
					return fmt.Errorf("local restart does not support --with-attachments; use --remote")
				}
				domain, err := app.SendDeploySignal(restartDomain)
				if err != nil {
					return fmt.Errorf("failed to trigger local restart via deploy signal: %w", err)
				}
				fmt.Println(styles.RenderSuccess(fmt.Sprintf("Restart signal sent for %s (local deploy path)", domain)))
				return nil
			}

			result, err := client.Restart(ctx, restartDomain, withAttachments)
			if err != nil {
				if !withAttachments && shouldFallbackToLocal(err) {
					domain, localErr := app.SendDeploySignal(restartDomain)
					if localErr == nil {
						fmt.Println(styles.RenderWarning(fmt.Sprintf("Remote restart failed (%v), used local deploy-signal fallback", err)))
						fmt.Println(styles.RenderSuccess(fmt.Sprintf("Restart signal sent for %s (local deploy path)", domain)))
						return nil
					}
				}
				return fmt.Errorf("failed to restart: %w", err)
			}
			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Restarted %s", result.Domain)))
			return nil
		},
	}

	cmd.Flags().BoolVar(&withAttachments, "with-attachments", false, "Also restart attached services (databases, caches)")

	return cmd
}
