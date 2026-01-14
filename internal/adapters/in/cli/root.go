// Package cli implements the CLI adapter for Gordon.
// This package provides Cobra commands that delegate to the app layer.
package cli

import (
	"context"

	"gordon/internal/app"

	"github.com/spf13/cobra"
)

var (
	// Version information (set at build time)
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// NewRootCmd creates the root command for Gordon CLI.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "gordon",
		Short: "Gordon - A lightweight container deployment platform",
		Long: `Gordon is a self-contained container deployment platform that combines
a Docker registry with automatic container deployment capabilities.

It listens for image pushes and automatically deploys containers based on
configuration rules, making it ideal for single-server deployments.`,
	}

	// Add subcommands
	rootCmd.AddCommand(newStartCmd())
	rootCmd.AddCommand(newReloadCmd())
	rootCmd.AddCommand(newLogsCmd())
	rootCmd.AddCommand(newVersionCmd())
	rootCmd.AddCommand(newAuthCmd())

	return rootCmd
}

// newStartCmd creates the start command.
func newStartCmd() *cobra.Command {
	var configPath string

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the Gordon server",
		Long:  `Start the Gordon server, including the registry and proxy components.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.Run(context.Background(), configPath)
		},
	}

	// Add flags
	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")

	return cmd
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
	var configPath string

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show Gordon process logs",
		Long: `Shows logs from the Gordon process. By default reads from the log file
configured in config.toml. If file logging is not enabled, falls back to
journalctl (if running as a systemd service).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return app.ShowLogs(configPath, follow, lines)
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of lines to show")

	return cmd
}

// SetVersionInfo sets the version information for the CLI.
func SetVersionInfo(version, commit, date string) {
	Version = version
	Commit = commit
	BuildDate = date
}
