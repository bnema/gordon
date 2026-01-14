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
		Short: "Reload containers with updated environment",
		Long: `Triggers a reload of all managed containers with updated environment
variables from their respective .env files.`,
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

// SetVersionInfo sets the version information for the CLI.
func SetVersionInfo(version, commit, date string) {
	Version = version
	Commit = commit
	BuildDate = date
}
