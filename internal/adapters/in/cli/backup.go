package cli

import (
	"context"
	"fmt"
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
			fmt.Println("DOMAIN\tDB\tSTATUS\tSTARTED_AT\tFILE_PATH")

			for _, job := range jobs {
				fmt.Printf("%s\t%s\t%s\t%s\t%s\n", job.Domain, job.DBName, job.Status, formatBackupTime(job.StartedAt), job.FilePath)
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

			fmt.Println("DOMAIN\tDB\tSTATUS\tSTARTED_AT\tFILE_PATH\tSIZE_BYTES")
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%d\n", result.Backup.Domain, result.Backup.DBName, result.Backup.Status, formatBackupTime(result.Backup.StartedAt), result.Backup.FilePath, result.Backup.SizeBytes)
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
			fmt.Println("NAME\tTYPE\tHOST\tPORT\tIMAGE")

			for _, db := range dbs {
				fmt.Printf("%s\t%s\t%s\t%d\t%s\n", db.Name, db.Type, db.Host, db.Port, db.ImageName)
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
			fmt.Println("DOMAIN\tDB\tSTATUS\tSTARTED_AT")

			for _, job := range jobs {
				fmt.Printf("%s\t%s\t%s\t%s\n", job.Domain, job.DBName, job.Status, formatBackupTime(job.StartedAt))
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
