package cli

import (
	"context"
	"fmt"
	"strings"

	"gordon/internal/adapters/in/cli/ui/components"
	"gordon/internal/adapters/in/cli/ui/styles"

	"github.com/spf13/cobra"
)

// newSecretsCmd creates the secrets command group.
func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage secrets",
		Long: `Manage secrets (environment variables) for routes.

Secrets are stored per-domain and injected into containers as environment variables.

When targeting a remote Gordon instance (via --target flag or GORDON_TARGET env var),
these commands operate on the remote server.`,
	}

	cmd.AddCommand(newSecretsListCmd())
	cmd.AddCommand(newSecretsSetCmd())
	cmd.AddCommand(newSecretsRemoveCmd())

	return cmd
}

// newSecretsListCmd creates the secrets list command.
func newSecretsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list <domain>",
		Short: "List secrets for a domain",
		Long: `List all secret keys configured for a domain.

Note: Only secret keys are shown, not values (for security).

Examples:
  gordon secrets list app.mydomain.com
  gordon --target https://gordon.mydomain.com secrets list api.mydomain.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			domain := args[0]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				fmt.Println(styles.RenderError("secrets command requires --target flag or GORDON_TARGET env var"))
				fmt.Println(styles.Theme.Muted.Render("Local secret management is not yet supported."))
				return nil
			}

			keys, err := client.ListSecrets(ctx, domain)
			if err != nil {
				return fmt.Errorf("failed to list secrets: %w", err)
			}

			if len(keys) == 0 {
				fmt.Println(styles.Theme.Muted.Render(fmt.Sprintf("No secrets configured for %s", domain)))
				return nil
			}

			fmt.Println(styles.Theme.Title.Render(fmt.Sprintf("Secrets for %s", domain)))
			fmt.Println()

			// Build table rows
			rows := make([][]string, len(keys))
			for i, key := range keys {
				rows[i] = []string{key, styles.Theme.Muted.Render("(hidden)")}
			}

			// Render table
			table := components.NewTable(
				components.WithColumns([]components.TableColumn{
					{Title: "Key", Width: 30},
					{Title: "Value", Width: 20},
				}),
				components.WithRows(rows),
			)

			fmt.Println(table.View())

			return nil
		},
	}
}

// newSecretsSetCmd creates the secrets set command.
func newSecretsSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <domain> <KEY=value>...",
		Short: "Set secrets for a domain",
		Long: `Set one or more secrets for a domain.

Secrets are specified as KEY=value pairs. Multiple secrets can be set at once.

Examples:
  gordon secrets set app.mydomain.com DATABASE_URL=postgres://localhost/db
  gordon secrets set app.mydomain.com API_KEY=secret123 DEBUG=false
  gordon --target https://gordon.mydomain.com secrets set api.mydomain.com TOKEN=abc`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			domain := args[0]
			pairs := args[1:]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				fmt.Println(styles.RenderError("secrets command requires --target flag or GORDON_TARGET env var"))
				return nil
			}

			// Parse KEY=value pairs
			secrets := make(map[string]string)
			for _, pair := range pairs {
				parts := strings.SplitN(pair, "=", 2)
				if len(parts) != 2 {
					fmt.Println(styles.RenderError(fmt.Sprintf("Invalid format: %s (expected KEY=value)", pair)))
					return nil
				}
				secrets[parts[0]] = parts[1]
			}

			if err := client.SetSecrets(ctx, domain, secrets); err != nil {
				return fmt.Errorf("failed to set secrets: %w", err)
			}

			if len(secrets) == 1 {
				for key := range secrets {
					fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret set: %s", key)))
				}
			} else {
				fmt.Println(styles.RenderSuccess(fmt.Sprintf("Set %d secrets for %s", len(secrets), domain)))
			}

			return nil
		},
	}
}

// newSecretsRemoveCmd creates the secrets remove command.
func newSecretsRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <domain> <key>",
		Short: "Remove a secret",
		Long: `Remove a secret from a domain.

Examples:
  gordon secrets remove app.mydomain.com OLD_API_KEY
  gordon secrets remove app.mydomain.com OLD_API_KEY --force`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			domain := args[0]
			key := args[1]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				fmt.Println(styles.RenderError("secrets command requires --target flag or GORDON_TARGET env var"))
				return nil
			}

			// Confirm unless --force
			if !force {
				confirmed, err := components.RunConfirm(
					fmt.Sprintf("Remove secret '%s' from %s?", key, domain),
				)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Println(styles.Theme.Muted.Render("Cancelled"))
					return nil
				}
			}

			if err := client.DeleteSecret(ctx, domain, key); err != nil {
				return fmt.Errorf("failed to remove secret: %w", err)
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret removed: %s", key)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}
