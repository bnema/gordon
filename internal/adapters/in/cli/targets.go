package cli

import (
	"fmt"

	"gordon/internal/adapters/in/cli/remote"
	"gordon/internal/adapters/in/cli/ui/components"
	"gordon/internal/adapters/in/cli/ui/styles"

	"github.com/spf13/cobra"
)

// newTargetsCmd creates the targets command group.
func newTargetsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "targets",
		Short: "Manage saved remote targets",
		Long: `Manage saved remote Gordon targets.

Targets allow you to save frequently used remote Gordon instances
and quickly switch between them.

Configuration is stored in ~/.config/gordon/targets.toml`,
	}

	cmd.AddCommand(newTargetsListCmd())
	cmd.AddCommand(newTargetsAddCmd())
	cmd.AddCommand(newTargetsRemoveCmd())
	cmd.AddCommand(newTargetsUseCmd())

	return cmd
}

// newTargetsListCmd creates the targets list command.
func newTargetsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved targets",
		RunE: func(cmd *cobra.Command, args []string) error {
			targets, active, err := remote.ListTargets()
			if err != nil {
				return fmt.Errorf("failed to list targets: %w", err)
			}

			if len(targets) == 0 {
				fmt.Println(styles.Theme.Muted.Render("No targets configured"))
				fmt.Println()
				fmt.Println("Add a target with:")
				fmt.Println(styles.Theme.Bold.Render("  gordon targets add <name> <url>"))
				return nil
			}

			fmt.Println(styles.Theme.Title.Render("Saved Targets"))
			fmt.Println()

			// Build table rows
			rows := make([][]string, 0, len(targets))
			for name, target := range targets {
				status := ""
				if name == active {
					status = styles.Theme.Success.Render("active")
				}

				// Mask token if present
				tokenStatus := styles.Theme.Muted.Render("none")
				if target.Token != "" {
					tokenStatus = styles.Theme.Success.Render("set")
				} else if target.TokenEnv != "" {
					tokenStatus = fmt.Sprintf("$%s", target.TokenEnv)
				}

				rows = append(rows, []string{name, target.URL, tokenStatus, status})
			}

			// Render table
			table := components.NewTable(
				components.WithColumns([]components.TableColumn{
					{Title: "Name", Width: 15},
					{Title: "URL", Width: 35},
					{Title: "Token", Width: 15},
					{Title: "Status", Width: 10},
				}),
				components.WithRows(rows),
			)

			fmt.Println(table.View())

			return nil
		},
	}
}

// newTargetsAddCmd creates the targets add command.
func newTargetsAddCmd() *cobra.Command {
	var token string
	var tokenEnv string

	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Add a new target",
		Long: `Add a new saved target.

Examples:
  gordon targets add prod https://gordon.mydomain.com
  gordon targets add prod https://gordon.mydomain.com --token eyJ...
  gordon targets add staging https://staging.mydomain.com --token-env STAGING_TOKEN`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			url := args[1]

			// If token-env is provided, use that instead of token
			finalToken := token
			if tokenEnv != "" {
				// Store token_env reference instead of actual token
				if err := addTargetWithEnv(name, url, tokenEnv); err != nil {
					return fmt.Errorf("failed to add target: %w", err)
				}
			} else {
				if err := remote.AddTarget(name, url, finalToken); err != nil {
					return fmt.Errorf("failed to add target: %w", err)
				}
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Target added: %s -> %s", name, url)))

			if token == "" && tokenEnv == "" {
				fmt.Println(styles.Theme.Muted.Render("Tip: Add a token with --token or --token-env for authenticated access"))
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&token, "token", "", "Authentication token")
	cmd.Flags().StringVar(&tokenEnv, "token-env", "", "Environment variable containing token")

	return cmd
}

// addTargetWithEnv adds a target with a token environment variable reference.
func addTargetWithEnv(name, url, tokenEnv string) error {
	config, err := remote.LoadTargets("")
	if err != nil {
		return err
	}

	config.Targets[name] = remote.TargetEntry{
		URL:      url,
		TokenEnv: tokenEnv,
	}

	return remote.SaveTargets("", config)
}

// newTargetsRemoveCmd creates the targets remove command.
func newTargetsRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a target",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Check if target exists
			targets, active, err := remote.ListTargets()
			if err != nil {
				return fmt.Errorf("failed to list targets: %w", err)
			}

			if _, exists := targets[name]; !exists {
				fmt.Println(styles.RenderError(fmt.Sprintf("Target '%s' not found", name)))
				return nil
			}

			// Confirm unless --force
			if !force {
				message := fmt.Sprintf("Remove target '%s'?", name)
				if name == active {
					message = fmt.Sprintf("Remove target '%s'? (currently active)", name)
				}

				confirmed, err := components.RunConfirm(message)
				if err != nil {
					return err
				}
				if !confirmed {
					fmt.Println(styles.Theme.Muted.Render("Cancelled"))
					return nil
				}
			}

			if err := remote.RemoveTarget(name); err != nil {
				return fmt.Errorf("failed to remove target: %w", err)
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Target removed: %s", name)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

// newTargetsUseCmd creates the targets use command.
func newTargetsUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the active target",
		Long: `Set a target as the active default.

When a target is active, it will be used automatically for remote commands
without needing to specify --target.

Examples:
  gordon targets use prod
  gordon routes list  # Uses prod target automatically`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if err := remote.SetActiveTarget(name); err != nil {
				if err.Error() == fmt.Sprintf("target '%s' not found", name) {
					fmt.Println(styles.RenderError(fmt.Sprintf("Target '%s' not found", name)))
					fmt.Println()
					fmt.Println("Available targets:")

					targets, _, _ := remote.ListTargets()
					for targetName := range targets {
						fmt.Printf("  %s\n", targetName)
					}
					return nil
				}
				return fmt.Errorf("failed to set active target: %w", err)
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Active target set to '%s'", name)))
			return nil
		},
	}
}
