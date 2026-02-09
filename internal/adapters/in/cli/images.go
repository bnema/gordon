package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
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
		Short: "List runtime images",
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
	cmd.Flags().IntVar(&opts.KeepLast, "keep", 0, "Number of latest tags to keep per repository (0 disables registry cleanup)")
	cmd.Flags().BoolVar(&opts.RuntimeOnly, "runtime-only", false, "Prune dangling runtime images only (skip registry cleanup)")

	return cmd
}

func runImagesList(ctx context.Context, client imagesClient, out io.Writer) error {
	images, err := client.ListImages(ctx)
	if err != nil {
		return fmt.Errorf("failed to list images: %w", err)
	}

	if len(images) == 0 {
		if _, err := fmt.Fprintln(out, "No images found"); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "Total images: 0 (dangling: 0)")
		return err
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "REPOSITORY\tTAG\tSIZE\tCREATED\tIMAGE_ID\tDANGLING"); err != nil {
		return err
	}

	danglingCount := 0
	for _, img := range images {
		dangling := "no"
		if img.Dangling {
			dangling = "yes"
			danglingCount++
		}

		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", img.Repository, img.Tag, formatImageSize(img.Size), formatImageCreatedAt(img.Created), img.ID, dangling); err != nil {
			return err
		}
	}

	if err := w.Flush(); err != nil {
		return err
	}

	_, err = fmt.Fprintf(out, "\nTotal images: %d (dangling: %d)\n", len(images), danglingCount)
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

		if _, err := fmt.Fprintln(out, "Dry run: no changes applied"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "Runtime: would prune %d dangling runtime images\n", danglingCount); err != nil {
			return err
		}
		if opts.RuntimeOnly {
			_, err = fmt.Fprintln(out, "Registry cleanup skipped (--runtime-only)")
			return err
		}
		if keepLast == 0 {
			_, err = fmt.Fprintln(out, "Registry cleanup skipped (--keep=0)")
			return err
		}

		_, err = fmt.Fprintf(out, "Registry: would keep last %d tags per repository\n", keepLast)
		return err
	}

	resp, err := client.PruneImages(ctx, keepLast)
	if err != nil {
		return fmt.Errorf("failed to prune images: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("failed to prune images: empty response")
	}

	if _, err := fmt.Fprintf(out, "Runtime: deleted=%d space_reclaimed=%d\n", resp.Runtime.DeletedCount, resp.Runtime.SpaceReclaimed); err != nil {
		return err
	}

	if opts.RuntimeOnly {
		_, err = fmt.Fprintln(out, "Registry cleanup skipped (--runtime-only)")
		return err
	}

	_, err = fmt.Fprintf(out, "Registry: tags_removed=%d blobs_removed=%d space_reclaimed=%d\n", resp.Registry.TagsRemoved, resp.Registry.BlobsRemoved, resp.Registry.SpaceReclaimed)
	return err
}

func formatImageCreatedAt(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}

func formatImageSize(size int64) string {
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
