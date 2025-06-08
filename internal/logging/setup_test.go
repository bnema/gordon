package logging

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gordon/internal/config"
)

func TestSetup_LoggingDisabled(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled: false,
		},
	}

	err := Setup(cfg)
	require.NoError(t, err)

	// Should have console loggers set
	assert.NotNil(t, MainLogger)
	assert.NotNil(t, ProxyLogger)
}

func TestSetup_LoggingEnabled(t *testing.T) {
	// Create temporary directory for test logs
	tempDir := t.TempDir()

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MainLogFile:       "gordon.log",
			ProxyLogFile:      "proxy.log",
			Level:             "info",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          true,
		},
	}

	err := Setup(cfg)
	require.NoError(t, err)

	// Check directories were created
	assert.DirExists(t, tempDir)
	assert.DirExists(t, filepath.Join(tempDir, "containers"))

	// Check directory permissions (allow for different umask settings)
	info, err := os.Stat(tempDir)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsDir())

	containerDirInfo, err := os.Stat(filepath.Join(tempDir, "containers"))
	require.NoError(t, err)
	assert.True(t, containerDirInfo.Mode().IsDir())

	// Check loggers are initialized
	assert.NotNil(t, MainLogger)
	assert.NotNil(t, ProxyLogger)

	// Check global log level was set
	assert.Equal(t, zerolog.InfoLevel, zerolog.GlobalLevel())
}

func TestSetup_InvalidLogLevel(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MainLogFile:       "gordon.log",
			ProxyLogFile:      "proxy.log",
			Level:             "invalid_level",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	err := Setup(cfg)
	require.NoError(t, err)

	// Should default to info level
	assert.Equal(t, zerolog.InfoLevel, zerolog.GlobalLevel())
}

func TestSetup_CreateDirectoryError(t *testing.T) {
	// Try to create logs in a path that doesn't exist and can't be created
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               "/invalid/path/that/cannot/be/created",
			ContainerLogDir:   "containers",
			MainLogFile:       "gordon.log",
			ProxyLogFile:      "proxy.log",
			Level:             "info",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	err := Setup(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create logs directory")
}

func TestSetup_CreateContainerDirectoryError(t *testing.T) {
	tempDir := t.TempDir()
	
	// Create a file where the container directory should be
	containerPath := filepath.Join(tempDir, "containers")
	err := os.WriteFile(containerPath, []byte("blocking file"), 0644)
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MainLogFile:       "gordon.log",
			ProxyLogFile:      "proxy.log",
			Level:             "info",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	err = Setup(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create container logs directory")
}

func TestSetup_DifferentLogLevels(t *testing.T) {
	testCases := []struct {
		name     string
		level    string
		expected zerolog.Level
	}{
		{"debug level", "debug", zerolog.DebugLevel},
		{"info level", "info", zerolog.InfoLevel},
		{"warn level", "warn", zerolog.WarnLevel},
		{"error level", "error", zerolog.ErrorLevel},
		{"fatal level", "fatal", zerolog.FatalLevel},
		{"panic level", "panic", zerolog.PanicLevel},
		{"trace level", "trace", zerolog.TraceLevel},
		{"disabled level", "disabled", zerolog.Disabled},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tempDir := t.TempDir()

			cfg := &config.Config{
				Logging: config.LoggingConfig{
					Enabled:           true,
					Dir:               tempDir,
					ContainerLogDir:   "containers",
					MainLogFile:       "gordon.log",
					ProxyLogFile:      "proxy.log",
					Level:             tc.level,
					MaxSize:           10,
					MaxBackups:        3,
					MaxAge:            7,
					Compress:          false,
				},
			}

			err := Setup(cfg)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, zerolog.GlobalLevel())
		})
	}
}

func TestGetContainerLogWriter_LoggingDisabled(t *testing.T) {
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled: false,
		},
	}

	writer, err := GetContainerLogWriter(cfg, "container123", "test-container")
	assert.Error(t, err)
	assert.Nil(t, writer)
	assert.Contains(t, err.Error(), "logging is disabled")
}

func TestGetContainerLogWriter_Success(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          true,
		},
	}

	containerID := "container123"
	containerName := "test-container"

	writer, err := GetContainerLogWriter(cfg, containerID, containerName)
	require.NoError(t, err)
	require.NotNil(t, writer)

	// Check container log directory was created
	containerLogDir := filepath.Join(tempDir, "containers")
	assert.DirExists(t, containerLogDir)

	// Check directory permissions  
	info, err := os.Stat(containerLogDir)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsDir())

	// Check writer configuration
	expectedLogFile := filepath.Join(containerLogDir, "container123.log")
	assert.Equal(t, expectedLogFile, writer.Filename)
	assert.Equal(t, 10, writer.MaxSize)
	assert.Equal(t, 3, writer.MaxBackups)
	assert.Equal(t, 7, writer.MaxAge)
	assert.True(t, writer.Compress)

	// Check symlink was created
	symlinkPath := filepath.Join(containerLogDir, "test-container.log")
	_, err = os.Lstat(symlinkPath)
	assert.NoError(t, err, "Symlink should exist")

	// Verify symlink points to correct target
	target, err := os.Readlink(symlinkPath)
	require.NoError(t, err)
	assert.Equal(t, "container123.log", target)
}

func TestGetContainerLogWriter_WithoutContainerName(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	containerID := "container456"

	writer, err := GetContainerLogWriter(cfg, containerID, "")
	require.NoError(t, err)
	require.NotNil(t, writer)

	// Check no symlink was created
	containerLogDir := filepath.Join(tempDir, "containers")
	symlinkPath := filepath.Join(containerLogDir, ".log")
	_, err = os.Lstat(symlinkPath)
	assert.Error(t, err, "No symlink should exist for empty container name")
}

func TestGetContainerLogWriter_ContainerNameSameAsID(t *testing.T) {
	tempDir := t.TempDir()

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	containerID := "container789"

	writer, err := GetContainerLogWriter(cfg, containerID, containerID)
	require.NoError(t, err)
	require.NotNil(t, writer)

	// Check no symlink was created when name equals ID
	containerLogDir := filepath.Join(tempDir, "containers")
	symlinkPath := filepath.Join(containerLogDir, "container789.log")
	
	// The symlink path would be the same as the actual log file, so no symlink should be created
	info, err := os.Lstat(symlinkPath)
	if err == nil {
		// If it exists, it should be a regular file, not a symlink
		assert.Equal(t, 0, info.Mode()&os.ModeSymlink, "Should not be a symlink")
	}
}

func TestGetContainerLogWriter_ReplaceExistingSymlink(t *testing.T) {
	tempDir := t.TempDir()
	containerLogDir := filepath.Join(tempDir, "containers")
	err := os.MkdirAll(containerLogDir, 0700)
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	containerName := "test-container"
	symlinkPath := filepath.Join(containerLogDir, "test-container.log")

	// Create existing symlink
	err = os.Symlink("old-target.log", symlinkPath)
	require.NoError(t, err)

	// Create new container log writer
	containerID := "new-container123"
	writer, err := GetContainerLogWriter(cfg, containerID, containerName)
	require.NoError(t, err)
	require.NotNil(t, writer)

	// Check symlink was replaced
	target, err := os.Readlink(symlinkPath)
	require.NoError(t, err)
	assert.Equal(t, "new-container123.log", target)
}

func TestGetContainerLogWriter_CreateDirectoryError(t *testing.T) {
	// Create a file where the container directory should be
	tempDir := t.TempDir()
	containerPath := filepath.Join(tempDir, "containers")
	err := os.WriteFile(containerPath, []byte("blocking file"), 0644)
	require.NoError(t, err)

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	writer, err := GetContainerLogWriter(cfg, "container123", "test-container")
	assert.Error(t, err)
	assert.Nil(t, writer)
	assert.Contains(t, err.Error(), "failed to create container log directory")
}

func TestClose(t *testing.T) {
	// Test that Close() doesn't panic and can be called multiple times
	assert.NotPanics(t, func() {
		Close()
		Close() // Should be safe to call multiple times
	})
}