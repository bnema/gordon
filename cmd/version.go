package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Long:  `Display the Gordon version, commit hash, and build date.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Gordon %s\n", BuildVersion)
		fmt.Printf("Commit: %s\n", BuildCommit)
		fmt.Printf("Built: %s\n", BuildDate)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	versionCmd.Flags().BoolP("short", "s", false, "Show only version number")
	
	// Override run function to handle short flag
	originalRun := versionCmd.Run
	versionCmd.Run = func(cmd *cobra.Command, args []string) {
		if short, _ := cmd.Flags().GetBool("short"); short {
			fmt.Println(BuildVersion)
			return
		}
		originalRun(cmd, args)
	}
}