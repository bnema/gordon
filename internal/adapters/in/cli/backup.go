package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// newBackupCmd creates the backup command group.
func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage database backups",
		Long: `Manage database backups through Gordon's admin API.

These commands currently require remote mode with a configured target.`,
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
			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("backup commands require a configured remote target")
			}

			domainName := ""
			if len(args) == 1 {
				domainName = args[0]
			}

			jobs, err := client.ListBackups(context.Background(), domainName)
			if err != nil {
				return fmt.Errorf("failed to list backups: %w", err)
			}

			if len(jobs) == 0 {
				fmt.Println("No backups found")
				return nil
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "DOMAIN\tDB\tSTATUS\tSTARTED_AT\tFILE_PATH"); err != nil {
				return err
			}

			for _, job := range jobs {
				if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", job.Domain, job.DBName, job.Status, formatBackupTime(job.StartedAt), job.FilePath); err != nil {
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
			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("backup commands require a configured remote target")
			}

			result, err := client.RunBackup(context.Background(), args[0], dbName)
			if err != nil {
				return fmt.Errorf("failed to run backup: %w", err)
			}

			if result.Backup == nil {
				return fmt.Errorf("backup run completed without backup payload")
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(w, "DOMAIN\tDB\tSTATUS\tSTARTED_AT\tFILE_PATH\tSIZE_BYTES"); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\n", result.Backup.Domain, result.Backup.DBName, result.Backup.Status, formatBackupTime(result.Backup.StartedAt), result.Backup.FilePath, result.Backup.SizeBytes); err != nil {
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
			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("backup commands require a configured remote target")
			}

			dbs, err := client.DetectDatabases(context.Background(), args[0])
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
			client, isRemote := GetRemoteClient()
			if !isRemote {
				return fmt.Errorf("backup commands require a configured remote target")
			}

			jobs, err := client.BackupStatus(context.Background())
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
