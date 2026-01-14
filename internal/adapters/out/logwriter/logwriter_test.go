package logwriter

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Run("creates log directory", func(t *testing.T) {
		dir := t.TempDir()
		logDir := filepath.Join(dir, "containers")

		writer, err := New(Config{
			Dir:        logDir,
			MaxSize:    100,
			MaxBackups: 3,
			MaxAge:     28,
		})

		require.NoError(t, err)
		require.NotNil(t, writer)
		defer writer.Close()

		// Verify directory was created
		info, err := os.Stat(logDir)
		require.NoError(t, err)
		assert.True(t, info.IsDir())
	})

	t.Run("uses existing directory", func(t *testing.T) {
		logDir := t.TempDir()

		writer, err := New(Config{
			Dir:        logDir,
			MaxSize:    100,
			MaxBackups: 3,
			MaxAge:     28,
		})

		require.NoError(t, err)
		require.NotNil(t, writer)
		defer writer.Close()
	})
}

func TestStartLogging(t *testing.T) {
	t.Run("creates log file and writes data", func(t *testing.T) {
		logDir := t.TempDir()
		writer, err := New(Config{
			Dir:        logDir,
			MaxSize:    100,
			MaxBackups: 3,
			MaxAge:     28,
		})
		require.NoError(t, err)
		defer writer.Close()

		// Create a mock log stream with Docker multiplex format
		// Format: 1 byte stream type, 3 bytes reserved, 4 bytes size (big endian), then data
		logData := createDockerLogEntry(1, "Hello from container\n")
		logStream := io.NopCloser(bytes.NewReader(logData))

		err = writer.StartLogging(context.Background(), "container-123", "app.example.com", logStream)
		require.NoError(t, err)

		// Wait for the goroutine to process
		time.Sleep(100 * time.Millisecond)

		// Verify log file was created
		logFile := filepath.Join(logDir, "app_example_com.log")
		content, err := os.ReadFile(logFile)
		require.NoError(t, err)
		assert.Contains(t, string(content), "Hello from container")
	})

	t.Run("replaces existing log stream for same container", func(t *testing.T) {
		logDir := t.TempDir()
		writer, err := New(Config{
			Dir:        logDir,
			MaxSize:    100,
			MaxBackups: 3,
			MaxAge:     28,
		})
		require.NoError(t, err)
		defer writer.Close()

		// Start first logging
		logData1 := createDockerLogEntry(1, "First log\n")
		logStream1 := io.NopCloser(bytes.NewReader(logData1))
		err = writer.StartLogging(context.Background(), "container-123", "app.example.com", logStream1)
		require.NoError(t, err)

		time.Sleep(50 * time.Millisecond)

		// Start second logging for same container
		logData2 := createDockerLogEntry(1, "Second log\n")
		logStream2 := io.NopCloser(bytes.NewReader(logData2))
		err = writer.StartLogging(context.Background(), "container-123", "app.example.com", logStream2)
		require.NoError(t, err)

		time.Sleep(100 * time.Millisecond)

		// Should have both logs (file is appended)
		logFile := filepath.Join(logDir, "app_example_com.log")
		content, err := os.ReadFile(logFile)
		require.NoError(t, err)
		assert.Contains(t, string(content), "Second log")
	})
}

func TestStopLogging(t *testing.T) {
	t.Run("stops log collection", func(t *testing.T) {
		logDir := t.TempDir()
		writer, err := New(Config{
			Dir:        logDir,
			MaxSize:    100,
			MaxBackups: 3,
			MaxAge:     28,
		})
		require.NoError(t, err)
		defer writer.Close()

		// Use a pipe so we can control the stream
		pr, pw := io.Pipe()
		err = writer.StartLogging(context.Background(), "container-123", "app.example.com", pr)
		require.NoError(t, err)

		// Write some data
		go func() {
			pw.Write(createDockerLogEntry(1, "Test log\n"))
			// Keep pipe open until stopped
		}()

		time.Sleep(50 * time.Millisecond)

		// Stop logging
		err = writer.StopLogging("container-123")
		require.NoError(t, err)

		// Clean up pipe
		pw.Close()
	})

	t.Run("noop for non-existent container", func(t *testing.T) {
		logDir := t.TempDir()
		writer, err := New(Config{
			Dir:        logDir,
			MaxSize:    100,
			MaxBackups: 3,
			MaxAge:     28,
		})
		require.NoError(t, err)
		defer writer.Close()

		// Should not error
		err = writer.StopLogging("non-existent")
		require.NoError(t, err)
	})
}

func TestClose(t *testing.T) {
	t.Run("stops all log streams", func(t *testing.T) {
		logDir := t.TempDir()
		writer, err := New(Config{
			Dir:        logDir,
			MaxSize:    100,
			MaxBackups: 3,
			MaxAge:     28,
		})
		require.NoError(t, err)

		// Start multiple log streams
		for i := 0; i < 3; i++ {
			pr, pw := io.Pipe()
			containerID := "container-" + string(rune('a'+i))
			domain := "app" + string(rune('0'+i)) + ".example.com"
			err := writer.StartLogging(context.Background(), containerID, domain, pr)
			require.NoError(t, err)

			// Keep pipe alive briefly
			go func(pw *io.PipeWriter) {
				time.Sleep(50 * time.Millisecond)
				pw.Close()
			}(pw)
		}

		time.Sleep(25 * time.Millisecond)

		// Close should stop all
		err = writer.Close()
		require.NoError(t, err)

		// Verify all streams are gone
		assert.Empty(t, writer.streams)
	})
}

func TestSanitizeDomain(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"app.example.com", "app_example_com"},
		{"api.v1.example.com", "api_v1_example_com"},
		{"localhost:8080", "localhost_8080"},
		{"app/service", "app_service"},
		{"app with spaces", "app_with_spaces"},
		{"simple", "simple"},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			result := sanitizeDomain(tc.input)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestMultipleContainers(t *testing.T) {
	logDir := t.TempDir()
	writer, err := New(Config{
		Dir:        logDir,
		MaxSize:    100,
		MaxBackups: 3,
		MaxAge:     28,
	})
	require.NoError(t, err)
	defer writer.Close()

	// Start logging for multiple containers
	containers := []struct {
		id     string
		domain string
		msg    string
	}{
		{"container-1", "app1.example.com", "Log from app1"},
		{"container-2", "app2.example.com", "Log from app2"},
		{"container-3", "api.example.com", "Log from api"},
	}

	for _, c := range containers {
		logData := createDockerLogEntry(1, c.msg+"\n")
		logStream := io.NopCloser(bytes.NewReader(logData))
		err := writer.StartLogging(context.Background(), c.id, c.domain, logStream)
		require.NoError(t, err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Verify each container has its own log file
	for _, c := range containers {
		filename := strings.ReplaceAll(c.domain, ".", "_") + ".log"
		logFile := filepath.Join(logDir, filename)
		content, err := os.ReadFile(logFile)
		require.NoError(t, err, "Log file should exist for %s", c.domain)
		assert.Contains(t, string(content), c.msg)
	}
}

// createDockerLogEntry creates a Docker multiplexed log entry.
// streamType: 0=stdin, 1=stdout, 2=stderr
func createDockerLogEntry(streamType byte, data string) []byte {
	size := len(data)
	header := make([]byte, 8)
	header[0] = streamType
	// bytes 1-3 are reserved (zeros)
	header[4] = byte(size >> 24)
	header[5] = byte(size >> 16)
	header[6] = byte(size >> 8)
	header[7] = byte(size)

	return append(header, []byte(data)...)
}
