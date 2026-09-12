package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/bnema/gordon/internal/adapters/dto"
	"github.com/bnema/gordon/pkg/bytesize"
)

// newBackupCmd creates the backup command group. Backups are identified by
// app, service, and the declared database or volume: a domain is never an
// identity.
func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backups",
		Aliases: []string{"backup"},
		Short:   "Manage app backups",
		Long: `Manage app backups.

Backup targets come from the app manifest: a service declares its databases
and volumes, and which of them are backed up. Runs locally through the daemon's
authenticated Unix socket, or against a remote Gordon instance when --remote
targeting is configured.`,
	}

	cmd.AddCommand(newBackupListCmd())
	cmd.AddCommand(newBackupRunCmd())
	cmd.AddCommand(newBackupStatusCmd())
	cmd.AddCommand(newBackupVolumeCmd())

	return cmd
}

// newBackupVolumeCmd creates the `backup volume` group.
func newBackupVolumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage app volume backups",
	}
	cmd.AddCommand(newVolumeBackupListCmd())
	cmd.AddCommand(newVolumeBackupRunCmd())
	cmd.AddCommand(newVolumeBackupStatusCmd())
	return cmd
}

func newBackupListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list [APP]",
		Short: "List database backups",
		Long:  cliRenderMuted("List stored backups for all apps, or for one app."),
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := ""
			if len(args) == 1 {
				app = args[0]
			}
			handle, err := resolveControlPlane(cliConfigPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runBackupList(cmd.Context(), handle.plane, app, cmd.OutOrStdout(), jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runBackupList(ctx context.Context, plane ControlPlane, app string, out io.Writer, jsonOut bool) error {
	jobs, err := plane.ListBackups(ctx, app)
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}
	return printBackupJobs(out, jobs, jsonOut)
}

func printBackupJobs(out io.Writer, jobs []dto.BackupJob, jsonOut bool) error {
	if len(jobs) == 0 {
		if jsonOut {
			return writeJSON(out, []dto.BackupJob{})
		}
		return cliWriteLine(out, cliRenderMuted("No backups found"))
	}

	if jsonOut {
		return writeJSON(out, jobs)
	}

	if err := cliWriteLine(out, cliRenderTitle("Backups")); err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "APP\tSERVICE\tDATABASE\tSTATUS\tSTARTED_AT\tBACKUP_ID"); err != nil {
		return err
	}
	for _, job := range jobs {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			job.App, job.Service, job.Database, job.Status, formatBackupTime(job.StartedAt), job.ID); err != nil {
			return err
		}
	}
	return w.Flush()
}

func newBackupRunCmd() *cobra.Command {
	var service string
	var database string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "run APP",
		Short: "Run a database backup now",
		Long: cliRenderMuted(`Run the declared database backup of one app service.

--service and --database select the target. Omitting a selector is only
allowed when exactly one compatible target exists.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(cliConfigPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runBackupRun(cmd.Context(), handle.plane, args[0], service, database, cmd.OutOrStdout(), jsonOut)
		},
	}

	cmd.Flags().StringVar(&service, "service", "", "Service that declares the database")
	cmd.Flags().StringVar(&database, "database", "", "Declared database name (optional)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runBackupRun(ctx context.Context, plane ControlPlane, app, service, database string, out io.Writer, jsonOut bool) error {
	result, err := plane.RunBackup(ctx, app, service, database)
	if err != nil {
		return fmt.Errorf("failed to run backup: %w", err)
	}
	if result.Backup == nil {
		return fmt.Errorf("backup run completed without a backup payload")
	}
	if jsonOut {
		return writeJSON(out, result)
	}
	return printBackupJobs(out, []dto.BackupJob{*result.Backup}, false)
}

func newBackupStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show database backup status",
		Long:  cliRenderMuted("Show stored backups plus declared targets that have no completed backup yet."),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(cliConfigPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runBackupStatus(cmd.Context(), handle.plane, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runBackupStatus(ctx context.Context, plane ControlPlane, out io.Writer, jsonOut bool) error {
	jobs, err := plane.BackupStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get backup status: %w", err)
	}
	return printBackupJobs(out, jobs, jsonOut)
}

func newVolumeBackupListCmd() *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "list [APP]",
		Short: "List volume backups",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := ""
			if len(args) == 1 {
				app = args[0]
			}
			handle, err := resolveControlPlane(cliConfigPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runVolumeBackupList(cmd.Context(), handle.plane, app, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runVolumeBackupList(ctx context.Context, plane ControlPlane, app string, out io.Writer, jsonOut bool) error {
	jobs, err := plane.ListVolumeBackups(ctx, app)
	if err != nil {
		return fmt.Errorf("failed to list volume backups: %w", err)
	}
	return printVolumeBackupJobs(out, jobs, jsonOut)
}

func newVolumeBackupRunCmd() *cobra.Command {
	var service string
	var volume string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "run APP",
		Short: "Run a volume backup now",
		Long: cliRenderMuted(`Run the declared volume backup of one app service.

--service and --volume select the target. Omitting a selector is only
allowed when exactly one compatible target exists.`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(cliConfigPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runVolumeBackupRun(cmd.Context(), handle.plane, args[0], service, volume, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().StringVar(&service, "service", "", "Service that declares the volume")
	cmd.Flags().StringVar(&volume, "volume", "", "Declared volume name (optional)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runVolumeBackupRun(ctx context.Context, plane ControlPlane, app, service, volume string, out io.Writer, jsonOut bool) error {
	result, err := plane.RunVolumeBackups(ctx, app, service, volume)
	if err != nil {
		// Partial results are reported before the error: an operator must
		// see which volumes completed.
		if result != nil && len(result.Backups) > 0 {
			if printErr := printVolumeBackupJobs(out, result.Backups, jsonOut); printErr != nil {
				return printErr
			}
		}
		return fmt.Errorf("failed to run volume backup: %w", err)
	}
	if jsonOut {
		return writeJSON(out, result)
	}
	return printVolumeBackupJobs(out, result.Backups, false)
}

func newVolumeBackupStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show volume backup status",
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(cliConfigPath)
			if err != nil {
				return err
			}
			defer handle.close()
			return runVolumeBackupStatus(cmd.Context(), handle.plane, cmd.OutOrStdout(), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runVolumeBackupStatus(ctx context.Context, plane ControlPlane, out io.Writer, jsonOut bool) error {
	jobs, err := plane.VolumeBackupStatus(ctx)
	if err != nil {
		return fmt.Errorf("failed to get volume backup status: %w", err)
	}
	return printVolumeBackupJobs(out, jobs, jsonOut)
}

func printVolumeBackupJobs(out io.Writer, jobs []dto.VolumeBackupJob, jsonOut bool) error {
	if len(jobs) == 0 {
		if jsonOut {
			return writeJSON(out, []dto.VolumeBackupJob{})
		}
		return cliWriteLine(out, cliRenderMuted("No volume backups found"))
	}
	if jsonOut {
		return writeJSON(out, jobs)
	}
	if err := cliWriteLine(out, cliRenderTitle("Volume Backups")); err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "APP\tSERVICE\tVOLUME\tCOMPRESSION\tSTATUS\tSTARTED_AT\tSIZE\tARTIFACT"); err != nil {
		return err
	}
	for _, job := range jobs {
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			job.App, job.Service, job.VolumeName, job.Compression, job.Status,
			formatBackupTime(job.StartedAt), bytesize.Format(job.SizeBytes), job.ArtifactRef); err != nil {
			return err
		}
	}
	return w.Flush()
}

func formatBackupTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
