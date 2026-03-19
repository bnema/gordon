package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/in/cli/ui/styles"
	"github.com/bnema/gordon/internal/usecase/auto/preview"
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
	var (
		ttl      string
		noBuild  bool
		noData   bool
		platform string
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a preview environment",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// Resolve preview name from arg or git branch.
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

			previewTag := "preview-" + name

			fmt.Fprintf(out, "Creating preview: %s\n", styles.Theme.Bold.Render(name))

			// Resolve control plane (remote or local) — same as push.
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			// Detect image name and registry using the same logic as push.
			dockerfile := "Dockerfile"
			imageName, err := detectImageName(dockerfile)
			if err != nil {
				return err
			}

			fmt.Fprintf(out, "Image:  %s\n", styles.Theme.Bold.Render(imageName))

			// Resolve registry from routes.
			registry, _, _, err := resolveRoute(ctx, handle.plane, "", "", dockerfile)
			if err != nil {
				return fmt.Errorf("failed to resolve registry: %w", err)
			}

			// Build the image ref with the preview tag.
			previewRef := fmt.Sprintf("%s/%s:%s", registry, imageName, previewTag)
			fmt.Fprintf(out, "Tag:    %s\n", styles.Theme.Bold.Render(previewRef))

			// Build and push.
			imageOps, err := newImageOpsFn()
			if err != nil {
				return err
			}

			if !noBuild {
				if _, statErr := os.Stat(dockerfile); os.IsNotExist(statErr) {
					return fmt.Errorf("dockerfile not found: %s", dockerfile)
				}

				buildPlatform := platform
				buildArgs := buildImageArgs(ctx, previewTag, buildPlatform, dockerfile, nil, previewRef, previewRef)
				fmt.Fprintln(out, "\nBuilding image...")
				if err := imageOps.Build(ctx, buildArgs); err != nil {
					return err
				}
			}

			fmt.Fprintln(out, "Pushing...")
			if err := imageOps.Push(ctx, previewRef); err != nil {
				return fmt.Errorf("failed to push %s: %w", previewRef, err)
			}

			fmt.Fprintln(out, styles.RenderSuccess("Push complete"))
			fmt.Fprintf(out, "Preview %s will be created by the server's autopreview handler.\n",
				styles.Theme.Bold.Render(name))

			if ttl != "" {
				fmt.Fprintf(out, "Requested TTL: %s (server config controls actual TTL)\n", ttl)
			}
			if noData {
				fmt.Fprintf(out, "Data copy: skipped (--no-data)\n")
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&ttl, "ttl", "", "Override TTL (e.g., 72h)")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "Skip image build")
	cmd.Flags().BoolVar(&noData, "no-data", false, "Skip volume cloning")
	cmd.Flags().StringVar(&platform, "platform", "linux/amd64", "Target platform for build")
	return cmd
}

func newPreviewListCmd() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active preview environments",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("preview list requires --remote (local preview listing is not yet supported)")
			}

			previews, err := client.ListPreviews(ctx)
			if err != nil {
				return fmt.Errorf("failed to list previews: %w", err)
			}

			if jsonOutput {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(previews)
			}

			if len(previews) == 0 {
				fmt.Fprintln(out, "No active previews")
				return nil
			}

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tDOMAIN\tIMAGE\tCREATED\tEXPIRES"); err != nil {
				return err
			}
			now := time.Now()
			for _, p := range previews {
				remaining := time.Until(p.ExpiresAt).Truncate(time.Minute)
				expiresStr := remaining.String()
				if p.IsExpired(now) {
					expiresStr = "expired"
				}
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					p.Name,
					p.Domain,
					truncateImage(p.Image, 40),
					p.CreatedAt.Format(time.DateTime),
					expiresStr,
				); err != nil {
					return err
				}
			}
			return w.Flush()
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
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			name := args[0]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("preview delete requires --remote (local preview deletion is not yet supported)")
			}

			if err := client.DeletePreview(ctx, name); err != nil {
				return fmt.Errorf("failed to delete preview %q: %w", name, err)
			}

			fmt.Fprintln(out, styles.RenderSuccess(fmt.Sprintf("Preview %q deleted", name)))
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
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			name := args[0]

			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("preview extend requires --remote (local preview extend is not yet supported)")
			}

			if err := client.ExtendPreview(ctx, name, ttl); err != nil {
				return fmt.Errorf("failed to extend preview %q: %w", name, err)
			}

			fmt.Fprintln(out, styles.RenderSuccess(fmt.Sprintf("Preview %q extended by %s", name, ttl)))
			return nil
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "24h", "Additional TTL duration")
	return cmd
}

func detectGitBranch() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output() // #nosec G204
	if err != nil {
		return "", fmt.Errorf("detect git branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
