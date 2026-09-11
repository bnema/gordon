// Package cli implements the CLI adapter for Gordon.
// This package provides Cobra commands that delegate to the app layer.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/remote"
)

var (
	// Version information (set at build time)
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"

	// Global flags for remote targeting
	remoteFlag      string
	tokenFlag       string
	insecureTLSFlag bool
)

// Command group IDs
const (
	groupServer = "server"
	groupManage = "manage"
	groupClient = "client"
)

// NewRootCmd creates the root command for Gordon CLI.
func NewRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use: "gordon",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if tokenFlag == "" {
				return nil
			}
			token, err := readProtectedSecretFile(tokenFlag)
			if err != nil {
				return fmt.Errorf("read remote token: %w", err)
			}
			tokenFlag = token
			return nil
		},
		Short: "Gordon - A self-hosted container deployment platform",
		Long: `Gordon is a self-hosted container deployment platform: a private
container registry, an app runtime, and a reverse proxy.

Applications are declared in standalone TOML files and managed with
gordon apps; push transfers OCI content only and never deploys.

Commands are organized by where they run:
  Server-only:  Run on the machine hosting Gordon (serve, auth, ca)
  Management:   Work locally or remotely via --remote flag (apps, secrets, etc.)
  Client-only:  CLI utilities that don't require a running Gordon server`,
	}

	// Define command groups (order matters for help output)
	rootCmd.AddGroup(
		&cobra.Group{ID: groupServer, Title: "Server Commands (local only):"},
		&cobra.Group{ID: groupManage, Title: "Management Commands (local or --remote):"},
		&cobra.Group{ID: groupClient, Title: "Client Commands:"},
	)

	// Add persistent flags for remote targeting
	rootCmd.PersistentFlags().StringVarP(&remoteFlag, "remote", "r", "", "Remote name or URL (e.g., prod, https://gordon.mydomain.com)")
	rootCmd.PersistentFlags().StringVar(&tokenFlag, "token-file", "", "Read remote authentication token from a mode 0600 file")
	rootCmd.PersistentFlags().BoolVar(&insecureTLSFlag, "insecure", false, "Skip TLS certificate verification for remote HTTPS endpoints")

	// Server-only commands (must run on the Gordon host)
	serveCmd := newServeCmd()
	serveCmd.GroupID = groupServer
	rootCmd.AddCommand(serveCmd)

	authCmd := newAuthCmd()
	authCmd.GroupID = groupServer
	rootCmd.AddCommand(authCmd)

	caCmd := newCACmd()
	caCmd.GroupID = groupServer
	rootCmd.AddCommand(caCmd)

	secretsCmd := newSecretsCmd()
	secretsCmd.GroupID = groupManage
	rootCmd.AddCommand(secretsCmd)

	pushCmd := newPushCmd()
	pushCmd.GroupID = groupManage
	rootCmd.AddCommand(pushCmd)

	reloadCmd := newReloadCmd()
	reloadCmd.GroupID = groupManage
	rootCmd.AddCommand(reloadCmd)

	logsCmd := newLogsCmd()
	logsCmd.GroupID = groupManage
	rootCmd.AddCommand(logsCmd)

	statusCmd := newStatusCmd()
	statusCmd.GroupID = groupManage
	rootCmd.AddCommand(statusCmd)

	backupCmd := newBackupCmd()
	backupCmd.GroupID = groupManage
	rootCmd.AddCommand(backupCmd)

	imagesCmd := newImagesCmd()
	imagesCmd.GroupID = groupManage
	rootCmd.AddCommand(imagesCmd)

	configCmd := newConfigCmd()
	configCmd.GroupID = groupManage
	rootCmd.AddCommand(configCmd)

	networksCmd := newNetworksCmd()
	networksCmd.GroupID = groupManage
	rootCmd.AddCommand(networksCmd)

	volumesCmd := newVolumesCmd()
	volumesCmd.GroupID = groupManage
	rootCmd.AddCommand(volumesCmd)

	tlsCmd := newTLSCmd()
	tlsCmd.GroupID = groupManage
	rootCmd.AddCommand(tlsCmd)

	trafficCmd := newTrafficCmd()
	trafficCmd.GroupID = groupManage
	rootCmd.AddCommand(trafficCmd)

	appsCmd := newAppsCmd()
	appsCmd.GroupID = groupManage
	rootCmd.AddCommand(appsCmd)

	// Client-only commands (no server needed)
	remotesCmd := newRemotesCmd()
	remotesCmd.GroupID = groupClient
	rootCmd.AddCommand(remotesCmd)

	versionCmd := newVersionCmd()
	versionCmd.GroupID = groupClient
	rootCmd.AddCommand(versionCmd)

	// Put help and completion in the client group
	rootCmd.SetHelpCommandGroupID(groupClient)
	rootCmd.SetCompletionCommandGroupID(groupClient)

	return rootCmd
}

// GetRemoteClient returns a remote client if targeting a remote instance,
// or nil if running locally.
func GetRemoteClient() (*remote.Client, bool, error) {
	resolved, isRemote, err := remote.ResolveStrict(remoteFlag, tokenFlag, insecureTLSFlag)
	if err != nil {
		return nil, false, err
	}
	if !isRemote {
		return nil, false, nil
	}

	opts := remoteClientOptions(resolved.Token, resolved.InsecureTLS)
	client := remote.NewClient(resolved.URL, opts...)
	return client, true, nil
}

func remoteClientOptions(token string, insecureTLS bool) []remote.ClientOption {
	opts := make([]remote.ClientOption, 0, 2)
	if token != "" {
		opts = append(opts, remote.WithToken(token))
	}
	if insecureTLS {
		opts = append(opts, remote.WithInsecureTLS(true))
	}
	return opts
}

// IsRemoteMode returns true if CLI is targeting a remote Gordon instance.
func IsRemoteMode() bool {
	_, isRemote, err := remote.ResolveStrict(remoteFlag, tokenFlag, insecureTLSFlag)
	return err == nil && isRemote
}

// newReloadCmd creates the reload command.
func newReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Reload installation configuration (never activates app state)",
		Long: `Reloads installation-only settings (entrypoints, TLS, limits,
external routes) after editing gordon.toml.

Reload never activates pending app desired state, never re-resolves
image tags, and never starts app workloads. Apps are managed with
gordon apps (apply, deploy, lifecycle).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(cliConfigPath)
			if err != nil {
				return err
			}
			defer handle.close()
			if err := handle.plane.Reload(cmd.Context()); err != nil {
				return fmt.Errorf("failed to reload: %w", err)
			}
			return cliWriteLine(cmd.OutOrStdout(), cliRenderSuccess("Configuration reloaded successfully"))
		},
	}
}

// newVersionCmd creates the version command.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if err := cliWriteLine(out, cliRenderTitle("Gordon "+Version)); err != nil {
				return err
			}
			if err := cliWriteLine(out, cliRenderMeta("Commit:", Commit)); err != nil {
				return err
			}
			if err := cliWriteLine(out, cliRenderMeta("Build Date:", BuildDate)); err != nil {
				return err
			}
			return nil
		},
	}
}

// cliConfigPath for local operations. If empty, config is auto-discovered
// from standard locations (/etc/gordon/gordon.toml, ~/.config/gordon/gordon.toml, ./gordon.toml).
var cliConfigPath string

// newLogsCmd creates the logs command.
func newLogsCmd() *cobra.Command {
	var follow bool
	var lines int
	var logsConfigPath string

	cmd := &cobra.Command{
		Use:   "logs [domain]",
		Short: "Show logs (Gordon process or app-domain container)",
		Long: `Shows logs from the Gordon process or a specific container.

Without a domain argument, shows Gordon process logs.
With a domain argument, shows container logs for the app HTTP host
served by that domain (resolved through ACTIVE app state; stopped or
unknown hosts report "container not found").

Examples:
  gordon logs                    # Gordon process logs
  gordon logs -f                 # Follow process logs
  gordon logs myapp.example.com  # Container logs for the app serving myapp.example.com
  gordon logs myapp.example.com -f

For per-service app logs with follow/tail selection, use gordon apps logs APP.

Remote mode:
  gordon logs --remote https://gordon.mydomain.com --token $TOKEN
  gordon logs myapp.example.com --remote https://gordon.mydomain.com --token $TOKEN`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logDomain := ""
			if len(args) > 0 {
				logDomain = args[0]
			}
			return runLogs(cmd.Context(), logsConfigPath, logDomain, follow, lines, cmd.OutOrStdout())
		},
	}

	cmd.Flags().StringVarP(&logsConfigPath, "config", "c", "", "Path to config file")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of lines to show")

	return cmd
}

// runLogs handles the logs command logic.
func runLogs(ctx context.Context, logsConfigPath, logDomain string, follow bool, lines int, out io.Writer) error {
	if logDomain != "" {
		handle, err := resolveControlPlaneForDomain(ctx, logDomain)
		if err != nil {
			return err
		}
		defer handle.close()
		return runContainerLogs(ctx, handle.plane, logDomain, follow, lines, out)
	}

	handle, err := resolveControlPlane(logsConfigPath)
	if err != nil {
		return err
	}
	defer handle.close()
	return runProcessLogs(ctx, handle.plane, follow, lines, out)
}

func runProcessLogs(ctx context.Context, cp ControlPlane, follow bool, lines int, out io.Writer) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if follow {
		ch, err := cp.StreamProcessLogs(ctx, lines)
		if err != nil {
			return fmt.Errorf("failed to stream process logs: %w", err)
		}
		for line := range ch {
			if err := cliWriteLine(out, line); err != nil {
				return err
			}
		}
		return nil
	}

	logLines, err := cp.GetProcessLogs(ctx, lines)
	if err != nil {
		return fmt.Errorf("failed to get process logs: %w", err)
	}
	for _, line := range logLines {
		if err := cliWriteLine(out, line); err != nil {
			return err
		}
	}
	return nil
}

func runContainerLogs(ctx context.Context, cp ControlPlane, logDomain string, follow bool, lines int, out io.Writer) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if follow {
		ch, err := cp.StreamContainerLogs(ctx, logDomain, lines)
		if err != nil {
			return fmt.Errorf("failed to stream container logs: %w", err)
		}
		for line := range ch {
			if err := cliWriteLine(out, line); err != nil {
				return err
			}
		}
		return nil
	}

	logLines, err := cp.GetContainerLogs(ctx, logDomain, lines)
	if err != nil {
		return fmt.Errorf("failed to get container logs: %w", err)
	}
	for _, line := range logLines {
		if err := cliWriteLine(out, line); err != nil {
			return err
		}
	}
	return nil
}

// SetVersionInfo sets the version information for the CLI.
func SetVersionInfo(version, commit, date string) {
	Version = version
	Commit = commit
	BuildDate = date
}
