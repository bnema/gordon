package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/internal/adapters/in/cli/ui/components"
	"github.com/bnema/gordon/pkg/bytesize"
)

type volumesClient interface {
	ListVolumes(ctx context.Context) ([]dto.Volume, error)
	PruneVolumes(ctx context.Context, req dto.VolumePruneRequest) (*dto.VolumePruneResponse, error)
}

type volumesPruneOptions struct {
	DryRun    bool
	NoConfirm bool
	Json      bool
}

func newVolumesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volumes",
		Short:   "Manage volumes",
		GroupID: groupManage,
	}
	cmd.AddCommand(newVolumesListCmd())
	cmd.AddCommand(newVolumesPruneCmd())
	return cmd
}

func newVolumesListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all volumes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runVolumesList(cmd.Context(), handle.plane, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "output as JSON")
	return cmd
}

func newVolumesPruneCmd() *cobra.Command {
	var opts volumesPruneOptions
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove orphaned volumes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runVolumesPrune(cmd.Context(), handle.plane, opts, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show what would be removed")
	cmd.Flags().BoolVar(&opts.NoConfirm, "no-confirm", false, "skip confirmation prompt")
	cmd.Flags().BoolVar(&opts.Json, "json", false, "output as JSON")
	return cmd
}

func runVolumesList(ctx context.Context, client volumesClient, out io.Writer, jsonOut bool) error {
	volumes, err := client.ListVolumes(ctx)
	if err != nil {
		return err
	}

	if jsonOut {
		return writeJSON(out, volumes)
	}

	if len(volumes) == 0 {
		return cliWriteLine(out, "No volumes found.")
	}

	if err := cliWriteLine(out, cliRenderTitle("Volumes")); err != nil {
		return err
	}

	for _, v := range volumes {
		status := "orphaned"
		if v.InUse {
			status = "in-use"
		}
		containers := "-"
		if len(v.Containers) > 0 {
			containers = fmt.Sprintf("%v", v.Containers)
		}
		if err := cliWritef(out, "  %-30s %-10s %-10s %s\n", v.Name, status, bytesize.Format(v.Size), containers); err != nil {
			return err
		}
	}

	var inUse, orphaned int
	for _, v := range volumes {
		if v.InUse {
			inUse++
		} else {
			orphaned++
		}
	}
	return cliWritef(out, "\n%d volumes (%d in-use, %d orphaned)\n", len(volumes), inUse, orphaned)
}

func renderPrunePreview(out io.Writer, resp *dto.VolumePruneResponse) error {
	if err := cliWriteLine(out, cliRenderTitle("Volumes to remove")); err != nil {
		return err
	}
	for _, v := range resp.Volumes {
		if err := cliWritef(out, "  %s (%s)\n", v.Name, bytesize.Format(v.Size)); err != nil {
			return err
		}
	}
	return cliWritef(out, "\n%d volumes, %s total\n", resp.VolumesRemoved, bytesize.Format(resp.SpaceReclaimed))
}

func runVolumesPrune(ctx context.Context, client volumesClient, opts volumesPruneOptions, out io.Writer) error {
	preview, err := client.PruneVolumes(ctx, dto.VolumePruneRequest{DryRun: true})
	if err != nil {
		return err
	}

	if preview.VolumesRemoved == 0 {
		if opts.Json {
			return writeJSON(out, preview)
		}
		return cliWriteLine(out, "No orphaned volumes to remove.")
	}

	if opts.DryRun {
		if opts.Json {
			return writeJSON(out, preview)
		}
		return renderPrunePreview(out, preview)
	}

	if !opts.Json {
		if err := renderPrunePreview(out, preview); err != nil {
			return err
		}
	}

	if !opts.NoConfirm && !opts.Json {
		confirmed, err := components.RunConfirm("Remove these volumes?")
		if err != nil {
			return err
		}
		if !confirmed {
			return cliWriteLine(out, "Cancelled.")
		}
	}

	result, err := client.PruneVolumes(ctx, dto.VolumePruneRequest{DryRun: false})
	if err != nil {
		return err
	}

	if opts.Json {
		return writeJSON(out, result)
	}

	return cliWriteLine(out, cliRenderSuccess(fmt.Sprintf("Removed %d volumes, reclaimed %s", result.VolumesRemoved, bytesize.Format(result.SpaceReclaimed))))
}
