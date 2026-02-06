package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"

	"github.com/spf13/cobra"
)

// newSecretsCmd creates the secrets command group.
func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Manage secrets",
		Long: `Manage secrets (environment variables) for routes and attachments.

Secrets are stored per-domain and injected into containers as environment variables.
Use --attachment to target attachment containers (databases, caches, etc.).

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
	return &cobra.Command{
		Use:   "list <domain>",
		Short: "List secrets for a domain",
		Long: `List all secret keys configured for a domain.

Note: Only secret keys are shown, not values (for security).
Attachment secrets (for services like databases) are also displayed.

Examples:
  gordon secrets list app.mydomain.com
  gordon --remote https://gordon.mydomain.com secrets list api.mydomain.com`,
		Args: cobra.ExactArgs(1),
		RunE: runSecretsListCmd,
	}
}

// runSecretsListCmd executes the secrets list command.
func runSecretsListCmd(_ *cobra.Command, args []string) error {
	ctx := context.Background()
	secretDomain := args[0]

	keys, attachments, isRemote, err := fetchSecretsWithAttachments(ctx, secretDomain)
	if err != nil {
		return err
	}

	totalSecrets := len(keys)
	for _, att := range attachments {
		totalSecrets += len(att.Keys)
	}

	if totalSecrets == 0 {
		fmt.Println(styles.Theme.Muted.Render(fmt.Sprintf("No secrets configured for %s", secretDomain)))
		return nil
	}

	title := fmt.Sprintf("Secrets for %s", secretDomain)
	if !isRemote {
		title = fmt.Sprintf("Secrets for %s (local)", secretDomain)
	}
	fmt.Println(styles.Theme.Title.Render(title))
	fmt.Println()

	rows := buildSecretsTableRows(keys, attachments)

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

// fetchSecretsWithAttachments retrieves secrets from remote or local source.
func fetchSecretsWithAttachments(ctx context.Context, secretDomain string) ([]string, []remote.AttachmentSecrets, bool, error) {
	client, isRemote := GetRemoteClient()
	if isRemote {
		result, err := client.ListSecretsWithAttachments(ctx, secretDomain)
		if err != nil {
			return nil, nil, isRemote, fmt.Errorf("failed to list secrets: %w", err)
		}
		return result.Keys, result.Attachments, isRemote, nil
	}

	local, err := GetLocalServices(configPath)
	if err != nil {
		return nil, nil, isRemote, fmt.Errorf("failed to initialize local services: %w", err)
	}

	keys, domainAttachments, err := local.GetSecretService().ListKeysWithAttachments(ctx, secretDomain)
	if err != nil {
		return nil, nil, isRemote, fmt.Errorf("failed to list secrets: %w", err)
	}

	// Convert to remote.AttachmentSecrets format
	var attachments []remote.AttachmentSecrets
	for _, att := range domainAttachments {
		attachments = append(attachments, remote.AttachmentSecrets{
			Service: att.Service,
			Keys:    att.Keys,
		})
	}

	return keys, attachments, isRemote, nil
}

// buildSecretsTableRows builds table rows with tree structure for attachments.
func buildSecretsTableRows(keys []string, attachments []remote.AttachmentSecrets) [][]string {
	var rows [][]string

	// Domain secrets first
	for _, key := range keys {
		rows = append(rows, []string{key, styles.Theme.Muted.Render("(hidden)")})
	}

	// Attachment secrets with tree structure
	for i, att := range attachments {
		isLastAttachment := i == len(attachments)-1
		rows = append(rows, buildAttachmentRows(att, isLastAttachment)...)
	}

	return rows
}

// buildAttachmentRows builds table rows for a single attachment with tree structure.
func buildAttachmentRows(att remote.AttachmentSecrets, isLastAttachment bool) [][]string {
	var rows [][]string

	// Attachment header with tree prefix
	prefix := styles.IconTreeBranch + styles.IconTreeLine
	if isLastAttachment {
		prefix = styles.IconTreeLast + styles.IconTreeLine
	}

	serviceName := extractServiceName(att.Service)
	attachmentHeader := fmt.Sprintf("%s %s", prefix, styles.Theme.Muted.Render(fmt.Sprintf("[%s]", serviceName)))
	rows = append(rows, []string{attachmentHeader, ""})

	// Keys for this attachment with nested tree structure
	for j, key := range att.Keys {
		isLastKey := j == len(att.Keys)-1
		keyPrefix := getKeyPrefix(isLastAttachment, isLastKey)
		rows = append(rows, []string{keyPrefix + " " + key, styles.Theme.Muted.Render("(hidden)")})
	}

	return rows
}

// extractServiceName extracts a short service name from a container name.
// e.g., "gordon-git-bnema-dev-gitea-postgres" → "gitea-postgres"
func extractServiceName(containerName string) string {
	if !strings.HasPrefix(containerName, "gordon-") {
		return containerName
	}

	parts := strings.SplitN(containerName, "-", 2)
	if len(parts) <= 1 {
		return containerName
	}

	serviceName := parts[1]
	allParts := strings.Split(serviceName, "-")

	// Handle service names based on the number of segments explicitly
	if len(allParts) < 2 {
		// No additional segments; use the service name as-is.
		return serviceName
	}
	if len(allParts) == 2 {
		// Exactly two segments; the service name is already in the desired form.
		return strings.Join(allParts, "-")
	}

	// More than two segments: take the last two as the short service name (e.g., "gitea-postgres").
	return strings.Join(allParts[len(allParts)-2:], "-")
}

// getKeyPrefix returns the tree prefix for a key based on its position.
func getKeyPrefix(isLastAttachment, isLastKey bool) string {
	if isLastAttachment {
		// Parent is last, use space continuation
		if isLastKey {
			return "   " + styles.IconTreeLast + styles.IconTreeLine
		}
		return "   " + styles.IconTreeBranch + styles.IconTreeLine
	}
	// Parent has siblings, use vertical line continuation
	if isLastKey {
		return styles.IconTreeVert + "  " + styles.IconTreeLast + styles.IconTreeLine
	}
	return styles.IconTreeVert + "  " + styles.IconTreeBranch + styles.IconTreeLine
}

// newSecretsSetCmd creates the secrets set command.
func newSecretsSetCmd() *cobra.Command {
	var attachment string

	cmd := &cobra.Command{
		Use:   "set <domain> <KEY=value>...",
		Short: "Set secrets for a domain or attachment",
		Long: `Set one or more secrets for a domain or an attachment container.

Secrets are specified as KEY=value pairs. Multiple secrets can be set at once.

Use --attachment to target an attachment service (e.g., postgres, redis) instead
of the main domain container.

Examples:
  gordon secrets set app.mydomain.com DATABASE_URL=postgres://localhost/db
  gordon secrets set app.mydomain.com API_KEY=secret123 DEBUG=false
  gordon secrets set app.mydomain.com --attachment postgres POSTGRES_PASSWORD=secret
  gordon secrets set app.mydomain.com --attachment redis REDIS_PASSWORD=secret`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			secretDomain := args[0]
			pairs := args[1:]

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

			client, isRemote := GetRemoteClient()
			if isRemote {
				if attachment != "" {
					if err := client.SetAttachmentSecrets(ctx, secretDomain, attachment, secrets); err != nil {
						return fmt.Errorf("failed to set secrets: %w", err)
					}
				} else {
					if err := client.SetSecrets(ctx, secretDomain, secrets); err != nil {
						return fmt.Errorf("failed to set secrets: %w", err)
					}
				}
			} else {
				local, err := GetLocalServices(configPath)
				if err != nil {
					return fmt.Errorf("failed to initialize local services: %w", err)
				}
				if attachment != "" {
					if err := local.GetSecretService().SetAttachment(ctx, secretDomain, attachment, secrets); err != nil {
						return fmt.Errorf("failed to set secrets: %w", err)
					}
				} else {
					if err := local.GetSecretService().Set(ctx, secretDomain, secrets); err != nil {
						return fmt.Errorf("failed to set secrets: %w", err)
					}
				}
			}

			target := secretDomain
			if attachment != "" {
				target = fmt.Sprintf("%s [%s]", secretDomain, attachment)
			}

			if len(secrets) == 1 {
				for key := range secrets {
					fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret set: %s on %s", key, target)))
				}
			} else {
				fmt.Println(styles.RenderSuccess(fmt.Sprintf("Set %d secrets for %s", len(secrets), target)))
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&attachment, "attachment", "a", "", "Target an attachment service (e.g., postgres, redis)")

	return cmd
}

// newSecretsRemoveCmd creates the secrets remove command.
func newSecretsRemoveCmd() *cobra.Command {
	var (
		force      bool
		attachment string
	)

	cmd := &cobra.Command{
		Use:   "remove <domain> <key>",
		Short: "Remove a secret",
		Long: `Remove a secret from a domain or an attachment container.

Use --attachment to target an attachment service (e.g., postgres, redis) instead
of the main domain container.

Examples:
  gordon secrets remove app.mydomain.com OLD_API_KEY
  gordon secrets remove app.mydomain.com OLD_API_KEY --force
  gordon secrets remove app.mydomain.com --attachment postgres POSTGRES_PASSWORD`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			secretDomain := args[0]
			key := args[1]

			target := secretDomain
			if attachment != "" {
				target = fmt.Sprintf("%s [%s]", secretDomain, attachment)
			}

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

			client, isRemote := GetRemoteClient()
			if isRemote {
				if attachment != "" {
					if err := client.DeleteAttachmentSecret(ctx, secretDomain, attachment, key); err != nil {
						return fmt.Errorf("failed to remove secret: %w", err)
					}
				} else {
					if err := client.DeleteSecret(ctx, secretDomain, key); err != nil {
						return fmt.Errorf("failed to remove secret: %w", err)
					}
				}
			} else {
				local, err := GetLocalServices(configPath)
				if err != nil {
					return fmt.Errorf("failed to initialize local services: %w", err)
				}
				if attachment != "" {
					if err := local.GetSecretService().DeleteAttachment(ctx, secretDomain, attachment, key); err != nil {
						return fmt.Errorf("failed to remove secret: %w", err)
					}
				} else {
					if err := local.GetSecretService().Delete(ctx, secretDomain, key); err != nil {
						return fmt.Errorf("failed to remove secret: %w", err)
					}
				}
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Secret removed from %s: %s", target, key)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")
	cmd.Flags().StringVarP(&attachment, "attachment", "a", "", "Target an attachment service (e.g., postgres, redis)")

	return cmd
}
