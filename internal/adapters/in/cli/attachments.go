package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"

	"github.com/spf13/cobra"
)

// newAttachmentsCmd creates the attachments command group.
func newAttachmentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "attachments",
		Aliases: []string{"attach"},
		Short:   "Manage container attachments",
		Long: `Manage container attachments (databases, caches, queues) in the configuration.

Attachments are service dependencies that run alongside your application containers.
They are defined per-domain or per-network-group in the configuration.

When targeting a remote Gordon instance (via --remote flag or GORDON_REMOTE env var),
these commands operate on the remote server. Otherwise, they require access to
the local Gordon configuration.`,
	}

	cmd.AddCommand(newAttachmentsListCmd())
	cmd.AddCommand(newAttachmentsAddCmd())
	cmd.AddCommand(newAttachmentsRemoveCmd())

	return cmd
}

// newAttachmentsListCmd creates the attachments list command.
func newAttachmentsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [domain-or-group]",
		Short: "List configured attachments",
		Long: `List all configured attachments, or attachments for a specific domain/group.

Examples:
  gordon attachments list                    # List all attachments
  gordon attachments list app.example.com    # List attachments for domain
  gordon attachments list backend            # List attachments for network group`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			target := ""
			if len(args) > 0 {
				target = args[0]
			}

			client, isRemote := GetRemoteClient()
			if isRemote {
				return runAttachmentsListRemote(ctx, client, target)
			}
			return runAttachmentsListLocal(ctx, configPath, target)
		},
	}
}

// runAttachmentsListRemote lists attachments from a remote Gordon instance.
func runAttachmentsListRemote(ctx context.Context, client *remote.Client, target string) error {
	if target != "" {
		// List for specific target
		images, err := client.GetAttachmentsConfig(ctx, target)
		if err != nil {
			return fmt.Errorf("failed to get attachments: %w", err)
		}

		if len(images) == 0 {
			fmt.Printf("No attachments configured for '%s'\n", target)
			return nil
		}

		fmt.Println(styles.Theme.Title.Render(fmt.Sprintf("Attachments for %s", target)))
		fmt.Println()
		for _, img := range images {
			fmt.Printf("  %s\n", img)
		}
		return nil
	}

	// List all attachments
	attachments, err := client.GetAllAttachmentsConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to list attachments: %w", err)
	}

	if len(attachments) == 0 {
		fmt.Println(styles.Theme.Muted.Render("No attachments configured"))
		return nil
	}

	// Sort targets for consistent output
	targets := make([]string, 0, len(attachments))
	for t := range attachments {
		targets = append(targets, t)
	}
	sort.Strings(targets)

	fmt.Println(styles.Theme.Title.Render("Attachments"))
	fmt.Println()

	for _, t := range targets {
		images := attachments[t]
		fmt.Printf("%s\n", styles.Theme.Bold.Render(t))
		for _, img := range images {
			fmt.Printf("  %s\n", img)
		}
	}

	return nil
}

// runAttachmentsListLocal lists attachments from local configuration.
func runAttachmentsListLocal(ctx context.Context, cfgPath string, target string) error {
	local, err := GetLocalServices(cfgPath)
	if err != nil {
		return fmt.Errorf("failed to initialize local services: %w", err)
	}

	if target != "" {
		// List for specific target
		images, err := local.GetConfigService().GetAttachmentsFor(ctx, target)
		if err != nil {
			return fmt.Errorf("failed to get attachments: %w", err)
		}

		if len(images) == 0 {
			fmt.Printf("No attachments configured for '%s'\n", target)
			return nil
		}

		fmt.Println(styles.Theme.Title.Render(fmt.Sprintf("Attachments for %s (local)", target)))
		fmt.Println()
		for _, img := range images {
			fmt.Printf("  %s\n", img)
		}
		return nil
	}

	// List all attachments
	attachments := local.GetConfigService().GetAllAttachments(ctx)

	if len(attachments) == 0 {
		fmt.Println(styles.Theme.Muted.Render("No attachments configured"))
		return nil
	}

	// Sort targets for consistent output
	targets := make([]string, 0, len(attachments))
	for t := range attachments {
		targets = append(targets, t)
	}
	sort.Strings(targets)

	fmt.Println(styles.Theme.Title.Render("Attachments (local)"))
	fmt.Println()

	for _, t := range targets {
		images := attachments[t]
		fmt.Printf("%s\n", styles.Theme.Bold.Render(t))
		for _, img := range images {
			fmt.Printf("  %s\n", img)
		}
	}

	return nil
}

// newAttachmentsAddCmd creates the attachments add command.
func newAttachmentsAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <domain-or-group> <image>",
		Short: "Add an attachment",
		Long: `Add a container attachment to a domain or network group.

Note: Attachments require network_isolation.enabled = true in your configuration.
Without network isolation, containers use Docker's default bridge network which
does not provide DNS resolution - your app won't be able to reach attachments
by hostname (e.g., 'postgres:5432').

Examples:
  gordon attachments add app.example.com postgres:18
  gordon attachments add backend redis:7-alpine
  gordon --remote https://gordon.mydomain.com attachments add api.mydomain.com memcached:latest`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			target := args[0]
			image := args[1]

			client, isRemote := GetRemoteClient()
			if isRemote {
				if err := client.AddAttachment(ctx, target, image); err != nil {
					return fmt.Errorf("failed to add attachment: %w", err)
				}
			} else {
				local, err := GetLocalServices(configPath)
				if err != nil {
					return fmt.Errorf("failed to initialize local services: %w", err)
				}

				// Warn if network isolation is disabled
				if !local.GetConfigService().IsNetworkIsolationEnabled() {
					fmt.Println(styles.RenderWarning("network_isolation.enabled = false"))
					fmt.Println(styles.Theme.Muted.Render("  Attachments require network isolation for DNS resolution."))
					fmt.Println(styles.Theme.Muted.Render("  Without it, your app cannot reach attachments by hostname."))
					fmt.Println(styles.Theme.Muted.Render("  Enable with: [network_isolation] enabled = true"))
					fmt.Println()
				}

				if err := local.GetConfigService().AddAttachment(ctx, target, image); err != nil {
					return fmt.Errorf("failed to add attachment: %w", err)
				}
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Attachment added: %s -> %s", target, image)))
			return nil
		},
	}

	return cmd
}

// newAttachmentsRemoveCmd creates the attachments remove command.
func newAttachmentsRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "remove <domain-or-group> <image>",
		Short: "Remove an attachment",
		Long: `Remove a container attachment from a domain or network group.

Examples:
  gordon attachments remove app.example.com postgres:15
  gordon attachments remove backend redis:7-alpine --force`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			target := args[0]
			image := args[1]

			// Confirm unless --force
			if !force {
				confirmed, err := components.RunConfirm(
					fmt.Sprintf("Remove attachment '%s' from '%s'?", image, target),
					components.WithDescription("This will remove the attachment from the configuration. The container will be stopped on next reload."),
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
				if err := client.RemoveAttachment(ctx, target, image); err != nil {
					return fmt.Errorf("failed to remove attachment: %w", err)
				}
			} else {
				local, err := GetLocalServices(configPath)
				if err != nil {
					return fmt.Errorf("failed to initialize local services: %w", err)
				}
				if err := local.GetConfigService().RemoveAttachment(ctx, target, image); err != nil {
					return fmt.Errorf("failed to remove attachment: %w", err)
				}
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Attachment removed: %s -> %s", target, image)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}
