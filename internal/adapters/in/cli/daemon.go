package cli

import "github.com/spf13/cobra"

// newDaemonCmd groups commands that inspect or operate the Gordon daemon
// itself rather than the apps it runs. They target the local daemon or the
// one selected with --remote.
func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Inspect and operate the Gordon daemon",
		Long: `Inspect and operate the Gordon daemon: status, process logs,
configuration, TLS, traffic, and networks.

Targets the local daemon, or the one selected with --remote.`,
	}

	cmd.AddCommand(
		newStatusCmd(),
		newLogsCmd(),
		newReloadCmd(),
		newConfigCmd(),
		newTLSCmd(),
		newTrafficCmd(),
		newNetworksCmd(),
	)

	return cmd
}
