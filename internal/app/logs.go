// Package app provides the application initialization and wiring.
package app

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
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

// tailFile reads the last N lines from a file using a ring buffer.
// This is memory-efficient as it only keeps the last N lines in memory,
// regardless of total file size.
func tailFile(path string, lines int) error {
	if lines <= 0 {
		return nil
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	// Use a ring buffer to store only the last N lines in memory
	buffer := make([]string, lines)
	index := 0
	total := 0

	for scanner.Scan() {
		buffer[index] = scanner.Text()
		index = (index + 1) % lines
		total++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to read log file: %w", err)
	}

	if total == 0 {
		return nil
	}

	// Print lines in correct order
	if total <= lines {
		// Fewer than N lines read; buffer is partially filled, in order from index 0
		for i := 0; i < total; i++ {
			fmt.Println(buffer[i])
		}
	} else {
		// More than N lines read; buffer is full ring starting at index
		for i := 0; i < lines; i++ {
			pos := (index + i) % lines
			fmt.Println(buffer[pos])
		}
	}

	return nil
}

// followFile watches a file for new content and prints it.
// It handles interrupt signals (Ctrl+C) gracefully to ensure proper cleanup.
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

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	reader := bufio.NewReader(file)

	// Poll for new content with signal checking
	for {
		select {
		case <-sigChan:
			// Graceful exit on interrupt
			fmt.Println() // Print newline for cleaner terminal output
			return nil
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				// No new content, wait and retry
				time.Sleep(100 * time.Millisecond)
				continue
			}
			fmt.Print(line)
		}
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
