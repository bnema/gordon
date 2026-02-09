package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// newBackupCmd creates the backup command group.
func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "backups",
		Aliases: []string{"backup"},
		Short:   "Manage database backups",
		Long: `Manage database backups.

Runs locally via in-process services by default, or against a remote Gordon
instance when --remote targeting is configured.`,
	}

	cmd.AddCommand(newBackupListCmd())
	cmd.AddCommand(newBackupRunCmd())
	cmd.AddCommand(newBackupDetectCmd())
	cmd.AddCommand(newBackupStatusCmd())

	return cmd
}

func newBackupListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [domain]",
		Short: "List backups",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			domainName := ""
			if len(args) == 1 {
				domainName = args[0]
			}

			jobs, err := handle.plane.ListBackups(cmd.Context(), domainName)
			if err != nil {
				return fmt.Errorf("failed to list backups: %w", err)
			}

			if len(jobs) == 0 {
				fmt.Println("No backups found")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "DOMAIN\tDB\tSTATUS\tSTARTED_AT\tBACKUP_ID"); err != nil {
				return err
			}

			for _, job := range jobs {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", job.Domain, job.DBName, job.Status, formatBackupTime(job.StartedAt), job.ID); err != nil {
					return err
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}

			return nil
		},
	}
}

func newBackupRunCmd() *cobra.Command {
	var dbName string

	cmd := &cobra.Command{
		Use:   "run <domain>",
		Short: "Run backup now",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			result, err := handle.plane.RunBackup(cmd.Context(), args[0], dbName)
			if err != nil {
				return fmt.Errorf("failed to run backup: %w", err)
			}

			if result.Backup == nil {
				return fmt.Errorf("backup run completed without backup payload")
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "DOMAIN\tDB\tSTATUS\tSTARTED_AT\tBACKUP_ID\tSIZE_BYTES"); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n", result.Backup.Domain, result.Backup.DBName, result.Backup.Status, formatBackupTime(result.Backup.StartedAt), result.Backup.ID, result.Backup.SizeBytes); err != nil {
				return err
			}
			if err := w.Flush(); err != nil {
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&dbName, "db", "", "Database attachment name (optional)")
	return cmd
}

func newBackupDetectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detect <domain>",
		Short: "Detect databases for domain",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			dbs, err := handle.plane.DetectDatabases(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("failed to detect databases: %w", err)
			}

			if len(dbs) == 0 {
				fmt.Println("No supported databases detected")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "NAME\tTYPE\tHOST\tPORT\tIMAGE"); err != nil {
				return err
			}

			for _, db := range dbs {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", db.Name, db.Type, db.Host, db.Port, db.ImageName); err != nil {
					return err
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}

			return nil
		},
	}
}

func newBackupStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show backup status",
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, err := resolveControlPlane(configPath)
			if err != nil {
				return err
			}
			defer handle.close()

			jobs, err := handle.plane.BackupStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to get backup status: %w", err)
			}

			if len(jobs) == 0 {
				fmt.Println("No backup status available")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "DOMAIN\tDB\tSTATUS\tSTARTED_AT"); err != nil {
				return err
			}

			for _, job := range jobs {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", job.Domain, job.DBName, job.Status, formatBackupTime(job.StartedAt)); err != nil {
					return err
				}
			}
			if err := w.Flush(); err != nil {
				return err
			}

			return nil
		},
	}
}

func formatBackupTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
