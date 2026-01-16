package cli

import (
	"fmt"

	"gordon/internal/adapters/in/cli/remote"
	"gordon/internal/adapters/in/cli/ui/components"
	"gordon/internal/adapters/in/cli/ui/styles"

	"github.com/spf13/cobra"
)

// newRemotesCmd creates the remotes command group.
func newRemotesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remotes",
		Short: "Manage saved remote Gordon instances",
		Long: `Manage saved remote Gordon instances.

Remotes allow you to save frequently used remote Gordon instances
and quickly switch between them.

Configuration is stored in ~/.config/gordon/remotes.toml`,
	}

	cmd.AddCommand(newRemotesListCmd())
	cmd.AddCommand(newRemotesAddCmd())
	cmd.AddCommand(newRemotesRemoveCmd())
	cmd.AddCommand(newRemotesUseCmd())

	return cmd
}

// newRemotesListCmd creates the remotes list command.
func newRemotesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved remotes",
		RunE: func(cmd *cobra.Command, args []string) error {
			remotes, active, err := remote.ListRemotes()
			if err != nil {
				return fmt.Errorf("failed to list remotes: %w", err)
			}

			if len(remotes) == 0 {
				fmt.Println(styles.Theme.Muted.Render("No remotes configured"))
				fmt.Println()
				fmt.Println("Add a remote with:")
				fmt.Println(styles.Theme.Bold.Render("  gordon remotes add <name> <url>"))
				return nil
			}

			fmt.Println(styles.Theme.Title.Render("Saved Remotes"))
			fmt.Println()

			// Build table rows
			rows := make([][]string, 0, len(remotes))
			for name, r := range remotes {
				status := ""
				if name == active {
					status = styles.Theme.Success.Render("active")
				}

				// Mask token if present
				tokenStatus := styles.Theme.Muted.Render("none")
				if r.Token != "" {
					tokenStatus = styles.Theme.Success.Render("set")
				} else if r.TokenEnv != "" {
					tokenStatus = fmt.Sprintf("$%s", r.TokenEnv)
				}

				rows = append(rows, []string{name, r.URL, tokenStatus, status})
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

// newRemotesAddCmd creates the remotes add command.
func newRemotesAddCmd() *cobra.Command {
	var token string
	var tokenEnv string

	cmd := &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Add a new remote",
		Long: `Add a new saved remote.

Examples:
  gordon remotes add prod https://gordon.mydomain.com
  gordon remotes add prod https://gordon.mydomain.com --token eyJ...
  gordon remotes add staging https://staging.mydomain.com --token-env STAGING_TOKEN`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			url := args[1]

			// If token-env is provided, use that instead of token
			finalToken := token
			if tokenEnv != "" {
				// Store token_env reference instead of actual token
				if err := addRemoteWithEnv(name, url, tokenEnv); err != nil {
					return fmt.Errorf("failed to add remote: %w", err)
				}
			} else {
				if err := remote.AddRemote(name, url, finalToken); err != nil {
					return fmt.Errorf("failed to add remote: %w", err)
				}
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Remote added: %s -> %s", name, url)))

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

// addRemoteWithEnv adds a remote with a token environment variable reference.
func addRemoteWithEnv(name, url, tokenEnv string) error {
	config, err := remote.LoadRemotes("")
	if err != nil {
		return err
	}

	config.Remotes[name] = remote.RemoteEntry{
		URL:      url,
		TokenEnv: tokenEnv,
	}

	return remote.SaveRemotes("", config)
}

// newRemotesRemoveCmd creates the remotes remove command.
func newRemotesRemoveCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a remote",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Check if remote exists
			remotes, active, err := remote.ListRemotes()
			if err != nil {
				return fmt.Errorf("failed to list remotes: %w", err)
			}

			if _, exists := remotes[name]; !exists {
				fmt.Println(styles.RenderError(fmt.Sprintf("Remote '%s' not found", name)))
				return nil
			}

			// Confirm unless --force
			if !force {
				message := fmt.Sprintf("Remove remote '%s'?", name)
				if name == active {
					message = fmt.Sprintf("Remove remote '%s'? (currently active)", name)
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

			if err := remote.RemoveRemote(name); err != nil {
				return fmt.Errorf("failed to remove remote: %w", err)
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Remote removed: %s", name)))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip confirmation")

	return cmd
}

// newRemotesUseCmd creates the remotes use command.
func newRemotesUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the active remote",
		Long: `Set a remote as the active default.

When a remote is active, it will be used automatically for remote commands
without needing to specify --remote.

Examples:
  gordon remotes use prod
  gordon routes list  # Uses prod remote automatically`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if err := remote.SetActiveRemote(name); err != nil {
				if err.Error() == fmt.Sprintf("remote '%s' not found", name) {
					fmt.Println(styles.RenderError(fmt.Sprintf("Remote '%s' not found", name)))
					fmt.Println()
					fmt.Println("Available remotes:")

					remotes, _, _ := remote.ListRemotes()
					for remoteName := range remotes {
						fmt.Printf("  %s\n", remoteName)
					}
					return nil
				}
				return fmt.Errorf("failed to set active remote: %w", err)
			}

			fmt.Println(styles.RenderSuccess(fmt.Sprintf("Active remote set to '%s'", name)))
			return nil
		},
	}
}
