package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/bnema/gordon/internal/usecase/auto/preview"
	"github.com/spf13/cobra"
)

func newPreviewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "preview [name]",
		Short: "Create or manage preview environments",
		Long:  "Build, push, and deploy an ephemeral preview environment. Defaults to current git branch name.",
	}

	createCmd := newPreviewCreateCmd()

	cmd.AddCommand(
		createCmd,
		newPreviewListCmd(),
		newPreviewDeleteCmd(),
		newPreviewExtendCmd(),
	)

	// Default action (no subcommand) = create
	cmd.RunE = createCmd.RunE
	cmd.Args = cobra.MaximumNArgs(1)
	cmd.Flags().AddFlagSet(createCmd.Flags())

	return cmd
}

func newPreviewCreateCmd() *cobra.Command {
	var ttl string
	var noBuild bool
	var noData bool

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a preview environment",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) > 0 {
				name = args[0]
			} else {
				branch, err := detectGitBranch()
				if err != nil {
					return fmt.Errorf("no preview name provided and could not detect git branch: %w", err)
				}
				name = preview.SanitizeBranchName(branch)
			}

			fmt.Printf("Creating preview: %s\n", name)
			// TODO: Reuse push mechanics (build, tag with preview-{name}, push)
			// TODO: Wire to remote API or local service
			_ = ttl
			_ = noBuild
			_ = noData
			return nil
		},
	}

	cmd.Flags().StringVar(&ttl, "ttl", "", "Override TTL (e.g., 72h)")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "Skip image build")
	cmd.Flags().BoolVar(&noData, "no-data", false, "Skip volume cloning")
	return cmd
}

func newPreviewListCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active preview environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Wire to local service or admin API
			fmt.Println("No active previews")
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func newPreviewDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a preview environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Wire to local service or admin API
			fmt.Printf("Deleting preview: %s\n", args[0])
			return nil
		},
	}
}

func newPreviewExtendCmd() *cobra.Command {
	var ttl string
	cmd := &cobra.Command{
		Use:   "extend <name>",
		Short: "Extend a preview environment's TTL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Wire to local service or admin API
			fmt.Printf("Extending preview %s by %s\n", args[0], ttl)
			return nil
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "24h", "Additional TTL duration")
	return cmd
}

func detectGitBranch() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output() // #nosec G204
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
