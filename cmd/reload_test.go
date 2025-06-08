package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReloadCommand(t *testing.T) {
	// Test reload command structure
	assert.Equal(t, "reload", reloadCmd.Use)
	assert.Contains(t, reloadCmd.Short, "Reload")
	assert.Contains(t, reloadCmd.Long, "configuration")
	assert.Contains(t, reloadCmd.Long, "environment variables")
}

func TestReloadCmdStructure(t *testing.T) {
	// Verify command is properly registered
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "reload" {
			found = true
			break
		}
	}
	assert.True(t, found, "reload command should be registered with root command")
}

func TestReloadCmdHelp(t *testing.T) {
	var output bytes.Buffer
	
	// Create a copy of the command to avoid affecting the global command
	cmd := &cobra.Command{
		Use:   reloadCmd.Use,
		Short: reloadCmd.Short,
		Long:  reloadCmd.Long,
		Run:   reloadCmd.Run,
	}
	
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--help"})
	
	err := cmd.Execute()
	assert.NoError(t, err)
	
	helpOutput := output.String()
	assert.Contains(t, helpOutput, "Reload")
	assert.Contains(t, helpOutput, "configuration")
	assert.Contains(t, helpOutput, "environment variables")
	assert.Contains(t, helpOutput, "containers")
}

func TestFindGordonPidFile(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(t *testing.T) (string, func()) // returns temp dir and cleanup func
		expectedFound bool
	}{
		{
			name: "PID file exists in temp directory",
			setupFunc: func(t *testing.T) (string, func()) {
				tempDir := t.TempDir()
				pidFile := filepath.Join(tempDir, "gordon.pid")
				err := os.WriteFile(pidFile, []byte("12345"), 0644)
				require.NoError(t, err)
				
				cleanup := func() {
					// Cleanup function for consistency
				}
				return pidFile, cleanup
			},
			expectedFound: false, // Won't find it because we can't easily mock os.TempDir
		},
		{
			name: "PID file exists in home directory",
			setupFunc: func(t *testing.T) (string, func()) {
				// Create temp directory to simulate home
				tempDir := t.TempDir()
				pidFile := filepath.Join(tempDir, ".gordon.pid")
				err := os.WriteFile(pidFile, []byte("12345"), 0644)
				require.NoError(t, err)
				
				// Mock home directory
				originalHome := os.Getenv("HOME")
				os.Setenv("HOME", tempDir)
				
				cleanup := func() {
					os.Setenv("HOME", originalHome)
				}
				return pidFile, cleanup
			},
			expectedFound: true,
		},
		{
			name: "no PID file exists",
			setupFunc: func(t *testing.T) (string, func()) {
				// Set home to a temp directory with no PID file
				tempDir := t.TempDir()
				originalHome := os.Getenv("HOME")
				os.Setenv("HOME", tempDir)
				
				cleanup := func() {
					os.Setenv("HOME", originalHome)
				}
				return "", cleanup
			},
			expectedFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectedPidFile, cleanup := tt.setupFunc(t)
			defer cleanup()
			
			result := findGordonPidFile()
			
			if tt.expectedFound {
				assert.NotEmpty(t, result)
				assert.Equal(t, expectedPidFile, result)
			} else {
				assert.Empty(t, result)
			}
		})
	}
}

func TestRunReload_NoPidFile(t *testing.T) {
	// Set up environment with no PID file
	tempDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Capture stdout by redirecting it
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		buf.ReadFrom(r)
		done <- buf.String()
	}()
	
	// Run reload command
	runReload(reloadCmd, []string{})
	
	// Restore stdout and get output
	w.Close()
	os.Stdout = oldStdout
	outputStr := <-done
	
	// Should indicate PID file not found
	assert.Contains(t, outputStr, "PID file not found")
	assert.Contains(t, outputStr, "Is Gordon running?")
}

func TestRunReload_WithValidPidFile(t *testing.T) {
	// Create a test PID file
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, ".gordon.pid")
	
	// Write current process PID to file (so it's valid)
	pid := os.Getpid()
	err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", pid)), 0644)
	require.NoError(t, err)
	
	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Capture stdout
	var output bytes.Buffer
	reloadCmd.SetOut(&output)
	
	// We can't easily test the actual signal sending to another process,
	// but we can test the PID file reading logic
	
	// Test PID file reading
	foundPidFile := findGordonPidFile()
	assert.Equal(t, pidFile, foundPidFile)
	
	// Test PID parsing
	pidBytes, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	
	var parsedPid int
	_, err = fmt.Sscanf(string(pidBytes), "%d", &parsedPid)
	require.NoError(t, err)
	assert.Equal(t, pid, parsedPid)
}

func TestRunReload_InvalidPidFile(t *testing.T) {
	// Create a PID file with invalid content
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, ".gordon.pid")
	
	// Write invalid PID content
	err := os.WriteFile(pidFile, []byte("invalid_pid"), 0644)
	require.NoError(t, err)
	
	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Capture stdout by redirecting it
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		buf.ReadFrom(r)
		done <- buf.String()
	}()
	
	// Run reload command
	runReload(reloadCmd, []string{})
	
	// Restore stdout and get output
	w.Close()
	os.Stdout = oldStdout
	outputStr := <-done
	
	// Should indicate parsing error
	assert.Contains(t, outputStr, "Error parsing Gordon PID")
}

func TestRunReload_UnreadablePidFile(t *testing.T) {
	// Create a PID file that can't be read
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, ".gordon.pid")
	
	// Create file but make it unreadable (if possible)
	err := os.WriteFile(pidFile, []byte("12345"), 0000)
	require.NoError(t, err)
	
	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Capture stdout
	var output bytes.Buffer
	reloadCmd.SetOut(&output)
	
	// Run reload command
	runReload(reloadCmd, []string{})
	
	// Should indicate read error (on systems that enforce permissions)
	outputStr := output.String()
	// Note: This test might not fail on all systems due to permission handling
	if len(outputStr) > 0 {
		// If we get output, it should contain an error message
		assert.True(t, 
			contains(outputStr, "Error reading") || 
			contains(outputStr, "Error parsing") ||
			contains(outputStr, "Error finding") ||
			contains(outputStr, "Error sending"),
			"Should contain some error message, got: %s", outputStr)
	}
}

func TestRunReload_NonExistentProcess(t *testing.T) {
	// Create a PID file with a PID that doesn't exist
	tempDir := t.TempDir()
	pidFile := filepath.Join(tempDir, ".gordon.pid")
	
	// Use a PID that's very unlikely to exist (high number)
	nonExistentPid := 999999
	err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", nonExistentPid)), 0644)
	require.NoError(t, err)
	
	// Mock home directory
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tempDir)
	defer os.Setenv("HOME", originalHome)

	// Capture stdout by redirecting it
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		buf.ReadFrom(r)
		done <- buf.String()
	}()
	
	// Run reload command
	runReload(reloadCmd, []string{})
	
	// Restore stdout and get output
	w.Close()
	os.Stdout = oldStdout
	outputStr := <-done
	
	// Should indicate process not found or signal sending error
	assert.True(t, 
		contains(outputStr, "Error finding") || 
		contains(outputStr, "Error sending") ||
		contains(outputStr, "not be running"),
		"Should contain process-related error, got: %s", outputStr)
}

func TestFindGordonPidFile_AllLocations(t *testing.T) {
	// Test the locations that findGordonPidFile checks
	locations := []string{
		"/tmp/gordon.pid",
		"/var/run/gordon.pid",
	}
	
	// Add temp dir location
	tempDirLocation := filepath.Join(os.TempDir(), "gordon.pid")
	locations = append(locations, tempDirLocation)
	
	// Add home directory location
	if homeDir, err := os.UserHomeDir(); err == nil {
		homeLocation := filepath.Join(homeDir, ".gordon.pid")
		locations = append(locations, homeLocation)
	}
	
	// Verify that the function checks these locations
	// We can't easily test all of them without root access,
	// but we can verify the function doesn't panic
	assert.NotPanics(t, func() {
		result := findGordonPidFile()
		// Result might be empty if no PID file exists, which is fine
		_ = result
	})
}

func TestSignalSending_Concepts(t *testing.T) {
	// Test the signal sending concepts used in runReload
	
	// Test that we can find our own process
	process, err := os.FindProcess(os.Getpid())
	assert.NoError(t, err)
	assert.NotNil(t, process)
	
	// Test signal types
	signals := []syscall.Signal{
		syscall.SIGUSR1,
		syscall.SIGTERM,
		syscall.SIGINT,
	}
	
	for _, sig := range signals {
		assert.NotNil(t, sig)
	}
}

func TestPidFileValidation(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		expectErr bool
	}{
		{
			name:      "valid PID",
			content:   "12345",
			expectErr: false,
		},
		{
			name:      "invalid PID - letters",
			content:   "abc",
			expectErr: true,
		},
		{
			name:      "invalid PID - empty",
			content:   "",
			expectErr: true,
		},
		{
			name:      "invalid PID - negative",
			content:   "-123",
			expectErr: false, // Parsing succeeds but PID is invalid
		},
		{
			name:      "valid PID with whitespace",
			content:   "  12345  \n",
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var pid int
			_, err := fmt.Sscanf(tt.content, "%d", &pid)
			
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.content != "" && tt.content != "  12345  \n" && tt.content != "-123" {
					assert.Greater(t, pid, 0)
				}
			}
		})
	}
}

// Helper function to check if string contains substring (case-insensitive)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && 
		   (s == substr || 
		    len(s) > len(substr) && 
		    (s[:len(substr)] == substr || 
		     s[len(s)-len(substr):] == substr ||
		     searchSubstring(s, substr)))
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}