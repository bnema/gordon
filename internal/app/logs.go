// Package app provides the application initialization and wiring.
package app

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
)

// ShowLogs displays Gordon process logs from file or journalctl.
func ShowLogs(configPath string, follow bool, lines int) error {
	// Try to find log file from config
	logPath, err := findLogFile(configPath)
	if err == nil && logPath != "" {
		return showFileLog(logPath, follow, lines)
	}

	// Fallback to journalctl
	if hasJournalctl() {
		return showJournalctlLog(follow, lines)
	}

	return fmt.Errorf("no logs available: enable file logging in config.toml or run gordon as a systemd service")
}

// findLogFile looks for the configured log file path.
func findLogFile(configPath string) (string, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("server.data_dir", DefaultDataDir())
	v.SetDefault("logging.file.enabled", false)
	v.SetDefault("logging.file.path", "")

	// Try to load config if path provided
	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return "", fmt.Errorf("failed to read config: %w", err)
		}
	} else {
		// Try default config locations
		v.SetConfigName("config")
		v.SetConfigType("toml")
		v.AddConfigPath(".")
		v.AddConfigPath(DefaultDataDir())
		v.AddConfigPath("/etc/gordon")
		_ = v.ReadInConfig() // Ignore error, we'll check if logging is enabled
	}

	if !v.GetBool("logging.file.enabled") {
		return "", nil
	}

	logPath := v.GetString("logging.file.path")
	if logPath == "" {
		return "", nil
	}

	// Make path absolute if relative
	if !filepath.IsAbs(logPath) {
		dataDir := v.GetString("server.data_dir")
		logPath = filepath.Join(dataDir, logPath)
	}

	// Check if file exists
	if _, err := os.Stat(logPath); err != nil {
		return "", fmt.Errorf("log file not found: %s", logPath)
	}

	return logPath, nil
}

// showFileLog displays logs from a file.
func showFileLog(logPath string, follow bool, lines int) error {
	// First show the last N lines
	if err := tailFile(logPath, lines); err != nil {
		return err
	}

	if !follow {
		return nil
	}

	// Follow mode: watch for new lines
	return followFile(logPath)
}

// tailFile reads the last N lines from a file.
func tailFile(path string, lines int) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	// Read all lines into a buffer
	var allLines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read log file: %w", err)
	}

	// Print last N lines
	start := 0
	if len(allLines) > lines {
		start = len(allLines) - lines
	}
	for _, line := range allLines[start:] {
		fmt.Println(line)
	}

	return nil
}

// followFile watches a file for new content and prints it.
func followFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	// Seek to end of file
	_, err = file.Seek(0, 2)
	if err != nil {
		return fmt.Errorf("failed to seek to end of file: %w", err)
	}

	reader := bufio.NewReader(file)

	// Poll for new content
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			// No new content, wait and retry
			time.Sleep(100 * time.Millisecond)
			continue
		}
		fmt.Print(line)
	}
}

// hasJournalctl checks if journalctl is available.
func hasJournalctl() bool {
	_, err := exec.LookPath("journalctl")
	return err == nil
}

// showJournalctlLog displays logs from journalctl.
func showJournalctlLog(follow bool, lines int) error {
	args := []string{"-u", "gordon", "-n", fmt.Sprintf("%d", lines), "--no-pager"}
	if follow {
		args = append(args, "-f")
	}

	// #nosec G204 -- args are constructed internally with controlled values
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		// Check if it's because the service doesn't exist
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
			return fmt.Errorf("gordon service not found in journalctl: enable file logging in config.toml or run gordon as a systemd service")
		}
		return fmt.Errorf("failed to run journalctl: %w", err)
	}

	return nil
}
