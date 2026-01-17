// Package cli implements the CLI adapter for Gordon.
// This package provides Cobra commands that delegate to the app layer.
package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"gordon/internal/adapters/in/cli/remote"
	"gordon/internal/app"
)

var (
	// Version information (set at build time)
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"

	// Global flags for remote targeting
	remoteFlag string
	tokenFlag  string
)

// NewRootCmd creates the root command for Gordon CLI.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "gordon",
		Short: "Gordon - A lightweight container deployment platform",
		Long: `Gordon is a self-contained container deployment platform that combines
a Docker registry with automatic container deployment capabilities.

It listens for image pushes and automatically deploys containers based on
configuration rules, making it ideal for single-server deployments.

The CLI can target remote Gordon instances using the --remote flag or
GORDON_REMOTE environment variable.`,
	}

	// Add persistent flags for remote targeting
	rootCmd.PersistentFlags().StringVar(&remoteFlag, "remote", "", "Remote Gordon URL (e.g., https://gordon.mydomain.com)")
	rootCmd.PersistentFlags().StringVar(&tokenFlag, "token", "", "Authentication token for remote")

	// Server commands
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newStartCmd()) // Deprecated alias for serve
	rootCmd.AddCommand(newReloadCmd())
	rootCmd.AddCommand(newDeployCmd())
	rootCmd.AddCommand(newLogsCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newAuthCmd())

	// Remote management commands
	rootCmd.AddCommand(newRoutesCmd())
	rootCmd.AddCommand(newSecretsCmd())
	rootCmd.AddCommand(newRemotesCmd())
	rootCmd.AddCommand(newStatusCmd())

	return rootCmd
}

// GetRemoteClient returns a remote client if targeting a remote instance,
// or nil if running locally.
func GetRemoteClient() (*remote.Client, bool) {
	url, token, isRemote := remote.ResolveRemote(remoteFlag, tokenFlag)
	if !isRemote {
		return nil, false
	}

	client := remote.NewClient(url, remote.WithToken(token))
	return client, true
}

// IsRemoteMode returns true if CLI is targeting a remote Gordon instance.
func IsRemoteMode() bool {
	_, _, isRemote := remote.ResolveRemote(remoteFlag, tokenFlag)
	return isRemote
}

// newReloadCmd creates the reload command.
func newReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Start containers for configured routes",
		Long: `Starts containers for routes defined in config.toml that don't have
a running container. Running containers are never restarted to ensure 100% uptime.

Use this command after editing config.toml to add new routes, or after pushing
images to the registry when the route was not yet configured.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReload()
		},
	}
}

// newVersionCmd creates the version command.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Printf("Gordon %s\n", Version)
			cmd.Printf("Commit: %s\n", Commit)
			cmd.Printf("Build Date: %s\n", BuildDate)
		},
	}
}

// runReload sends SIGUSR1 to the running Gordon process.
func runReload() error {
	return app.SendReloadSignal()
}

// newLogsCmd creates the logs command.
func newLogsCmd() *cobra.Command {
	var follow bool
	var lines int
	var logsConfigPath string

	cmd := &cobra.Command{
		Use:   "logs [domain]",
		Short: "Show logs (Gordon process or container)",
		Long: `Shows logs from the Gordon process or a specific container.

Without a domain argument, shows Gordon process logs.
With a domain argument, shows container logs for that domain.

Examples:
  gordon logs                    # Gordon process logs
  gordon logs -f                 # Follow process logs
  gordon logs myapp.local        # Container logs for myapp.local
  gordon logs myapp.local -f     # Follow container logs

Remote mode:
  gordon logs --remote https://gordon.mydomain.com --token $TOKEN
  gordon logs myapp.local --remote https://gordon.mydomain.com --token $TOKEN`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logDomain := ""
			if len(args) > 0 {
				logDomain = args[0]
			}
			return runLogs(logsConfigPath, logDomain, follow, lines)
		},
	}

	cmd.Flags().StringVarP(&logsConfigPath, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of lines to show")

	return cmd
}

// runLogs handles the logs command logic.
func runLogs(logsConfigPath, logDomain string, follow bool, lines int) error {
	client, isRemote := GetRemoteClient()
	if isRemote {
		return runLogsRemote(client, logDomain, follow, lines)
	}
	return runLogsLocal(logsConfigPath, logDomain, follow, lines)
}

// runLogsRemote fetches logs from a remote Gordon instance.
func runLogsRemote(client *remote.Client, logDomain string, follow bool, lines int) error {
	ctx := context.Background()

	if follow {
		return streamLogsRemote(ctx, client, logDomain, lines)
	}

	if logDomain == "" {
		// Process logs
		logLines, err := client.GetProcessLogs(ctx, lines)
		if err != nil {
			return fmt.Errorf("failed to get process logs: %w", err)
		}
		for _, line := range logLines {
			fmt.Println(line)
		}
	} else {
		// Container logs
		logLines, err := client.GetContainerLogs(ctx, logDomain, lines)
		if err != nil {
			return fmt.Errorf("failed to get container logs: %w", err)
		}
		for _, line := range logLines {
			fmt.Println(line)
		}
	}
	return nil
}

// streamLogsRemote streams logs from a remote Gordon instance.
func streamLogsRemote(ctx context.Context, client *remote.Client, logDomain string, lines int) error {
	var ch <-chan string
	var err error

	if logDomain == "" {
		ch, err = client.StreamProcessLogs(ctx, lines)
	} else {
		ch, err = client.StreamContainerLogs(ctx, logDomain, lines)
	}
	if err != nil {
		return fmt.Errorf("failed to stream logs: %w", err)
	}

	for line := range ch {
		fmt.Println(line)
	}
	return nil
}

// runLogsLocal shows logs from a local Gordon instance.
func runLogsLocal(logsConfigPath, logDomain string, follow bool, lines int) error {
	if logDomain == "" {
		// Process logs - use existing app.ShowLogs
		return app.ShowLogs(logsConfigPath, follow, lines)
	}

	// Container logs - use local services
	return showContainerLogsLocal(logsConfigPath, logDomain, follow, lines)
}

// showContainerLogsLocal shows container logs using local Docker access.
func showContainerLogsLocal(logsConfigPath, logDomain string, follow bool, lines int) error {
	// For local container logs, we need Docker access which requires
	// the runtime to be initialized. For now, suggest using remote mode
	// or direct docker logs command.
	fmt.Printf("Container logs for %s\n", logDomain)
	fmt.Println("To view container logs locally, use:")
	fmt.Printf("  docker logs --tail %d %s\n", lines, logDomain)
	if follow {
		fmt.Printf("  docker logs -f --tail %d %s\n", lines, logDomain)
	}
	fmt.Println("\nOr use remote mode to access logs via the admin API.")
	return nil
}

// SetVersionInfo sets the version information for the CLI.
func SetVersionInfo(version, commit, date string) {
	Version = version
	Commit = commit
	BuildDate = date
}
