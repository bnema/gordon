package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
)

var reloadCmd = &cobra.Command{
	Use:   "reload",
	Short: "Reload configuration and environment variables into running containers",
	Long: `Reload the Gordon configuration and environment variables from .env files into running containers.
This command reloads the config file and redeploys all containers with updated environment 
variables from their respective .env files. Containers are recreated to pick up the new configuration and environment variables.`,
	Run: runReload,
}

func init() {
	rootCmd.AddCommand(reloadCmd)
}

func runReload(cmd *cobra.Command, args []string) {
	// Find the Gordon PID file to get the process ID
	pidFile := findGordonPidFile()
	if pidFile == "" {
		fmt.Println("Error: Gordon PID file not found. Is Gordon running?")
		fmt.Println("Please start Gordon with 'gordon start' first.")
		return
	}

	// Read the PID from the file
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		fmt.Printf("Error reading Gordon PID file: %v\n", err)
		return
	}

	var pid int
	if _, err := fmt.Sscanf(string(pidBytes), "%d", &pid); err != nil {
		fmt.Printf("Error parsing Gordon PID: %v\n", err)
		return
	}

	// Check if the process is still running
	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("Error finding Gordon process: %v\n", err)
		return
	}

	// Send SIGUSR1 signal to trigger reload
	if err := process.Signal(syscall.SIGUSR1); err != nil {
		fmt.Printf("Error sending reload signal to Gordon: %v\n", err)
		fmt.Println("Gordon may not be running or may not have permission to send signals.")
		return
	}

	fmt.Println("Manual reload signal sent to Gordon successfully.")
	fmt.Println("Check Gordon logs to see the reload progress.")
}

func findGordonPidFile() string {
	// Look for PID file in common locations
	locations := []string{
		"/tmp/gordon.pid",
		"/var/run/gordon.pid",
		filepath.Join(os.TempDir(), "gordon.pid"),
	}

	// Also check in user's home directory
	if homeDir, err := os.UserHomeDir(); err == nil {
		locations = append(locations, filepath.Join(homeDir, ".gordon.pid"))
	}

	for _, location := range locations {
		if _, err := os.Stat(location); err == nil {
			return location
		}
	}

	return ""
}
