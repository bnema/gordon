package cli

import "github.com/spf13/cobra"

func newTrafficCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "traffic",
		Short: "Inspect remote traffic plane status",
	}
	cmd.AddCommand(newTrafficStatusCmd())
	return cmd
}
