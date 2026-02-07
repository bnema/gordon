package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// newBackupCmd creates the backup command group.
func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage database backups",
		Long: `Manage database backups through Gordon's admin API.

These commands currently require remote mode with --remote/--token
or configured remotes.`,
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
				return fmt.Errorf("backup commands currently require --remote and --token")
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

			for _, job := range jobs {
				fmt.Printf("%s\t%s\t%s\t%s\t%s\n", job.Domain, job.DBName, job.Status, job.StartedAt.Format("2006-01-02T15:04:05Z"), job.FilePath)
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
				return fmt.Errorf("backup commands currently require --remote and --token")
			}

			result, err := client.RunBackup(context.Background(), args[0], dbName)
			if err != nil {
				return fmt.Errorf("failed to run backup: %w", err)
			}

			fmt.Printf("Backup completed: domain=%s db=%s path=%s size=%d\n", result.Backup.Domain, result.Backup.DBName, result.Backup.FilePath, result.Backup.SizeBytes)
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
				return fmt.Errorf("backup commands currently require --remote and --token")
			}

			dbs, err := client.DetectDatabases(context.Background(), args[0])
			if err != nil {
				return fmt.Errorf("failed to detect databases: %w", err)
			}

			if len(dbs) == 0 {
				fmt.Println("No supported databases detected")
				return nil
			}

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
				return fmt.Errorf("backup commands currently require --remote and --token")
			}

			jobs, err := client.BackupStatus(context.Background())
			if err != nil {
				return fmt.Errorf("failed to get backup status: %w", err)
			}

			if len(jobs) == 0 {
				fmt.Println("No backup status available")
				return nil
			}

			for _, job := range jobs {
				fmt.Printf("%s\t%s\t%s\t%s\n", job.Domain, job.DBName, job.Status, job.StartedAt.Format("2006-01-02T15:04:05Z"))
			}

			return nil
		},
	}
}
