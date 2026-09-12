package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"

	"github.com/spf13/cobra"
)

// newSecretsCmd creates the secrets command group.
func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage installation secrets",
		Long: `Manage installation secrets (per-host entries such as service auth).

Application secret VALUES are managed with gordon apps secrets and stay
in pass under gordon/apps/<uuid>/<service>/<name>. The commands below
manage the installation secret store only.

When targeting a remote Gordon instance (via --remote flag or GORDON_REMOTE env var),
these commands operate on the remote server.`,
	}

	cmd.AddCommand(newSecretsListCmd())
	cmd.AddCommand(newSecretsSetCmd())
	cmd.AddCommand(newSecretsRemoveCmd())

	return cmd
}

// newSecretsListCmd creates the secrets list command.
func newSecretsListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list <domain>",
		Short: "List secrets for a domain",
		Long: `List all secret keys configured for a domain.

Note: Only secret keys are shown, not values (for security).

Examples:
  gordon secrets list app.mydomain.com
  gordon --remote https://gordon.mydomain.com secrets list api.mydomain.com`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsListCmd(cmd, args, jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

// runSecretsListCmd executes the secrets list command.
func runSecretsListCmd(cmd *cobra.Command, args []string, jsonOut bool) error {
	ctx := cmd.Context()
	secretDomain := args[0]

	handle, err := resolveControlPlaneForDomain(ctx, secretDomain)
	if err != nil {
		return err
	}
	defer handle.close()

	keys, err := fetchSecrets(ctx, handle.plane, secretDomain)
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		if jsonOut {
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"domain": secretDomain,
				"keys":   []string{},
			})
		}
		fmt.Println(styles.Theme.Muted.Render(fmt.Sprintf("No secrets configured for %s", secretDomain)))
		return nil
	}

	if jsonOut {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"domain": secretDomain,
			"keys":   keys,
		})
	}

	title := fmt.Sprintf("Secrets for %s", secretDomain)
	if !handle.isRemote {
		title = fmt.Sprintf("Secrets for %s (local)", secretDomain)
	}
	fmt.Println(styles.Theme.Title.Render(title))
	fmt.Println()

	rows := buildSecretsTableRows(keys)

	table := components.NewTable(
		components.WithColumns([]components.TableColumn{
			{Title: "Key", Width: 45},
			{Title: "Value", Width: 10},
		}),
		components.WithRows(rows),
	)

	fmt.Println(table.View())
	return nil
}

// fetchSecrets retrieves secret keys from the selected control plane.
func fetchSecrets(ctx context.Context, cp ControlPlane, secretDomain string) ([]string, error) {
	result, err := cp.ListSecrets(ctx, secretDomain)
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets: %w", err)
	}
	return result.Keys, nil
}

// buildSecretsTableRows builds table rows for secret keys.
func buildSecretsTableRows(keys []string) [][]string {
	var rows [][]string
	for _, key := range keys {
		rows = append(rows, []string{key, styles.Theme.Muted.Render("(hidden)")})
	}
	return rows
}

// newSecretsSetCmd creates the secrets set command.
func newSecretsSetCmd() *cobra.Command {
	var fromFile string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "set <domain> --from-file <path>",
		Short: "Set installation secrets for a domain",
		Long: `Set one or more installation secrets for a domain.

Secrets are read from a mode 0600 file containing one KEY=value pair per line.
For app secret values, use gordon apps secrets instead.

Examples:
  gordon secrets set app.mydomain.com --from-file ./app.env`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			secretDomain := args[0]
			content, err := readProtectedSecretFile(fromFile)
			if err != nil {
				return fmt.Errorf("read secrets: %w", err)
			}

			secrets := make(map[string]string)
			for _, pair := range strings.Split(content, "\n") {
				parts := strings.SplitN(pair, "=", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid format: %s (expected KEY=value)", pair)
				}
				secrets[parts[0]] = parts[1]
			}

			handle, err := resolveControlPlaneForDomain(ctx, secretDomain)
			if err != nil {
				return err
			}
			defer handle.close()
			if err := handle.plane.SetSecrets(ctx, secretDomain, secrets); err != nil {
				return fmt.Errorf("failed to set secrets: %w", err)
			}

			target := secretDomain

			if jsonOut {
				return writeJSON(cmd.OutOrStdout(), map[string]any{
					"domain": secretDomain, "count": len(secrets), "updated": true,
				})
			}
			if len(secrets) == 1 {
				for key := range secrets {
					return cliWriteLine(cmd.OutOrStdout(), styles.RenderSuccess(fmt.Sprintf("Secret set: %s on %s", key, target)))
				}
			}
			return cliWriteLine(cmd.OutOrStdout(), styles.RenderSuccess(fmt.Sprintf("Set %d secrets for %s", len(secrets), target)))
		},
	}

	cmd.Flags().StringVar(&fromFile, "from-file", "", "Read KEY=value lines from a mode 0600 file")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	_ = cmd.MarkFlagRequired("from-file")

	return cmd
}

// newSecretsRemoveCmd creates the secrets remove command.
func newSecretsRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <domain> <key>",
		Short: "Remove an installation secret",
		Long: `Remove an installation secret from a domain.

For app secret values, use gordon apps secrets delete instead.

Examples:
  gordon secrets remove app.mydomain.com OLD_API_KEY
  gordon secrets remove app.mydomain.com OLD_API_KEY --force`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			secretDomain := args[0]
			key := args[1]

			target := secretDomain

			// Confirm unless --force
			if !force {
				confirmed, err := components.RunConfirm(
					fmt.Sprintf("Remove secret '%s' from %s?", key, target),
				)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Println(styles.Theme.Muted.Render("Cancelled"))
					return nil
				}
			}

			handle, err := resolveControlPlaneForDomain(ctx, secretDomain)
			if err != nil {
				return err
			}
			defer handle.close()
			if err := handle.plane.DeleteSecret(ctx, secretDomain, key); err != nil {
				return fmt.Errorf("failed to remove secret: %w", err)
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret removed from %s: %s", target, key)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}
