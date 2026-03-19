package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/domain"
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
			name, err := resolvePreviewName(args)
			if err != nil {
				return err
			}

			previewTag := "preview-" + name

			if err := cliWriteLine(out, cliRenderTitle("Preview: "+name)); err != nil {
				return err
			}

			// Resolve control plane (remote or local) — same as push.
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			previewRef, err := resolvePreviewImageRef(ctx, handle.plane, out, previewTag)
			if err != nil {
				return err
			}

			if err := buildAndPushPreview(ctx, out, previewRef, previewTag, platform, noBuild); err != nil {
				return err
			}

			return printPreviewCreateSummary(out, name, ttl, noData)
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
				return writeJSON(out, previews)
			}

			if len(previews) == 0 {
				return cliWriteLine(out, cliRenderEmptyState("No active previews"))
			}

			return printPreviewTable(out, previews)
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

			return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Preview %q deleted", name)))
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

			return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Preview %q extended by %s", name, ttl)))
		},
	}
	cmd.Flags().StringVar(&ttl, "ttl", "24h", "Additional TTL duration")
	return cmd
}

func printPreviewTable(out io.Writer, previews []domain.PreviewRoute) error {
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
}

func resolvePreviewName(args []string) (string, error) {
	if len(args) > 0 {
		return args[0], nil
	}
	branch, err := detectGitBranch()
	if err != nil {
		return "", fmt.Errorf("no preview name provided and could not detect git branch: %w", err)
	}
	return preview.SanitizeBranchName(branch), nil
}

func resolvePreviewImageRef(ctx context.Context, cp ControlPlane, out io.Writer, previewTag string) (string, error) {
	dockerfile := "Dockerfile"
	imageName, err := detectImageName(dockerfile)
	if err != nil {
		return "", err
	}
	if err := cliWriteLine(out, cliRenderMeta("Image:", imageName)); err != nil {
		return "", err
	}

	registry, _, _, err := resolveRoute(ctx, cp, "", "", dockerfile)
	if err != nil {
		return "", fmt.Errorf("failed to resolve registry: %w", err)
	}

	previewRef := fmt.Sprintf("%s/%s:%s", registry, imageName, previewTag)
	if err := cliWriteLine(out, cliRenderMeta("Tag:", previewRef)); err != nil {
		return "", err
	}
	return previewRef, nil
}

func buildAndPushPreview(ctx context.Context, out io.Writer, previewRef, previewTag, platform string, noBuild bool) error {
	imageOps, err := newImageOpsFn()
	if err != nil {
		return err
	}

	if !noBuild {
		dockerfile := "Dockerfile"
		if _, statErr := os.Stat(dockerfile); os.IsNotExist(statErr) {
			return fmt.Errorf("dockerfile not found: %s", dockerfile)
		}
		buildArgs := buildImageArgs(ctx, previewTag, platform, dockerfile, nil, previewRef, previewRef)
		if err := cliWriteLine(out, "\nBuilding image..."); err != nil {
			return err
		}
		if err := imageOps.Build(ctx, buildArgs); err != nil {
			return err
		}
	}

	if err := cliWriteLine(out, "Pushing..."); err != nil {
		return err
	}
	if err := imageOps.Push(ctx, previewRef); err != nil {
		return fmt.Errorf("failed to push %s: %w", previewRef, err)
	}
	return nil
}

func printPreviewCreateSummary(out io.Writer, name, ttl string, noData bool) error {
	if err := cliWriteLine(out, cliRenderSuccess("Push complete")); err != nil {
		return err
	}
	if err := cliWritef(out, "Preview %s will be created by the server's autopreview handler.\n", name); err != nil {
		return err
	}
	if ttl != "" {
		if err := cliWritef(out, "Requested TTL: %s (server config controls actual TTL)\n", ttl); err != nil {
			return err
		}
	}
	if noData {
		if err := cliWriteLine(out, cliRenderInfo("Data copy: skipped (--no-data)")); err != nil {
			return err
		}
	}
	return nil
}

func detectGitBranch() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output() // #nosec G204
	if err != nil {
		return "", fmt.Errorf("detect git branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
