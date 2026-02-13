package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
)

type imagesClient interface {
	ListImages(ctx context.Context) ([]dto.Image, error)
	PruneImages(ctx context.Context, keepLast int) (*dto.ImagePruneResponse, error)
}

type imagesPruneOptions struct {
	DryRun      bool
	KeepLast    int
	RuntimeOnly bool
}

const (
	imagesListRepositoryColumnWidth = 44
	imagesListTagColumnWidth        = 14
	imagesListSizeColumnWidth       = 10
	imagesListCreatedColumnWidth    = 22
	imagesListImageIDColumnWidth    = 22
	imagesListDanglingColumnWidth   = 10
)

var imagesListTableColumns = []components.TableColumn{
	{Title: "REPOSITORY", Width: imagesListRepositoryColumnWidth},
	{Title: "TAG", Width: imagesListTagColumnWidth},
	{Title: "SIZE", Width: imagesListSizeColumnWidth},
	{Title: "CREATED", Width: imagesListCreatedColumnWidth},
	{Title: "IMAGE_ID", Width: imagesListImageIDColumnWidth},
	{Title: "DANGLING", Width: imagesListDanglingColumnWidth},
}

func newImagesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "images",
		Short: "List and prune images",
		Long: `Inspect and clean up runtime and registry images.

These commands currently require remote mode with a configured target.`,
	}

	cmd.AddCommand(newImagesListCmd())
	cmd.AddCommand(newImagesPruneCmd())

	return cmd
}

func newImagesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List runtime images and registry tags",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("images commands require a configured remote target")
			}

			return runImagesList(cmd.Context(), client, cmd.OutOrStdout())
		},
	}
}

func newImagesPruneCmd() *cobra.Command {
	var opts imagesPruneOptions

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune unused images",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("images commands require a configured remote target")
			}

			return runImagesPrune(cmd.Context(), client, opts, cmd.OutOrStdout())
		},
	}

	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "Show what would be pruned without applying changes")
	cmd.Flags().IntVar(&opts.KeepLast, "keep", 0, "Number of previous version tags to keep per repository (latest is always kept when present; 0 disables registry cleanup)")
	cmd.Flags().BoolVar(&opts.RuntimeOnly, "runtime-only", false, "Prune dangling runtime images only (skip registry cleanup)")

	return cmd
}

func runImagesList(ctx context.Context, client imagesClient, out io.Writer) error {
	images, err := client.ListImages(ctx)
	if err != nil {
		return fmt.Errorf("failed to list images: %w", err)
	}

	if len(images) == 0 {
		if err := cliWriteLine(out, cliRenderMuted("No images found")); err != nil {
			return err
		}
		err := cliWriteLine(out, cliRenderInfo("Total images: 0 (dangling: 0)"))
		return err
	}

	if err := cliWriteLine(out, cliRenderTitle("Images")); err != nil {
		return err
	}

	danglingCount := 0
	rows := make([][]string, 0, len(images))
	for _, img := range images {
		dangling := "no"
		if img.Dangling {
			dangling = "yes"
			danglingCount++
		}

		rows = append(rows, []string{
			img.Repository,
			img.Tag,
			formatImageSize(img.Size),
			formatImageCreatedAt(img.Created),
			formatImageID(img.ID),
			dangling,
		})
	}

	table := components.NewTable(
		components.WithColumns(imagesListTableColumns),
		components.WithRows(rows),
		components.WithHeaderStyle(lipgloss.NewStyle().Bold(true)),
		components.WithCellStyle(lipgloss.NewStyle()),
	)

	if err := cliWriteLine(out, table.View()); err != nil {
		return err
	}

	err = cliWritef(out, "\nTotal images: %d (dangling: %d)\n", len(images), danglingCount)
	return err
}

func runImagesPrune(ctx context.Context, client imagesClient, opts imagesPruneOptions, out io.Writer) error {
	if opts.KeepLast < 0 {
		return fmt.Errorf("--keep must be >= 0")
	}

	keepLast := opts.KeepLast
	if opts.RuntimeOnly {
		keepLast = 0
	}

	if opts.DryRun {
		images, err := client.ListImages(ctx)
		if err != nil {
			return fmt.Errorf("failed to list images: %w", err)
		}

		danglingCount := 0
		for _, img := range images {
			if img.Dangling {
				danglingCount++
			}
		}

		if err := cliWriteLine(out, cliRenderWarning("Dry run: no changes applied")); err != nil {
			return err
		}
		if err := cliWritef(out, "Runtime: would prune %d dangling runtime images\n", danglingCount); err != nil {
			return err
		}
		if opts.RuntimeOnly {
			err = cliWriteLine(out, cliRenderMuted("Registry cleanup skipped (--runtime-only)"))
			return err
		}
		if keepLast == 0 {
			err = cliWriteLine(out, cliRenderMuted("Registry cleanup skipped (--keep=0)"))
			return err
		}

		err = cliWritef(out, "Registry: would keep latest + %d previous tags per repository\n", keepLast)
		return err
	}

	resp, err := client.PruneImages(ctx, keepLast)
	if err != nil {
		return fmt.Errorf("failed to prune images: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("failed to prune images: empty response")
	}

	if err := cliWritef(out, "Runtime: deleted=%d space_reclaimed=%d\n", resp.Runtime.DeletedCount, resp.Runtime.SpaceReclaimed); err != nil {
		return err
	}

	if opts.RuntimeOnly {
		err = cliWriteLine(out, cliRenderMuted("Registry cleanup skipped (--runtime-only)"))
		return err
	}

	err = cliWritef(out, "Registry: tags_removed=%d blobs_removed=%d space_reclaimed=%d\n", resp.Registry.TagsRemoved, resp.Registry.BlobsRemoved, resp.Registry.SpaceReclaimed)
	return err
}

func formatImageCreatedAt(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func formatImageSize(size int64) string {
	if size <= 0 {
		return "-"
	}

	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

func formatImageID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "-"
	}

	return id
}
