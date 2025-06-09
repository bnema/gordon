package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
	"gordon/internal/config"
)

var (
	MainLogger  zerolog.Logger
	ProxyLogger zerolog.Logger
)

// Setup initializes the logging system based on the configuration
func Setup(cfg *config.Config) error {
	if !cfg.Logging.Enabled {
		// Keep console logging only
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
		MainLogger = log.Logger
		ProxyLogger = log.Logger
		return nil
	}

	// Create logs directory with secure permissions (0700 - owner only)
	if err := os.MkdirAll(cfg.Logging.Dir, 0700); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	// Create container logs directory with secure permissions
	containerLogDir := filepath.Join(cfg.Logging.Dir, cfg.Logging.ContainerLogDir)
	if err := os.MkdirAll(containerLogDir, 0700); err != nil {
		return fmt.Errorf("failed to create container logs directory: %w", err)
	}

	// Set log level
	level, err := zerolog.ParseLevel(cfg.Logging.Level)
	if err != nil {
		level = zerolog.InfoLevel
		log.Warn().Str("invalid_level", cfg.Logging.Level).Msg("Invalid log level, using info")
	}
	zerolog.SetGlobalLevel(level)

	// Configure time format
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	// Setup main application logger
	mainLogFile := filepath.Join(cfg.Logging.Dir, cfg.Logging.MainLogFile)
	mainFileWriter := &lumberjack.Logger{
		Filename:   mainLogFile,
		MaxSize:    cfg.Logging.MaxSize,
		MaxBackups: cfg.Logging.MaxBackups,
		MaxAge:     cfg.Logging.MaxAge,
		Compress:   cfg.Logging.Compress,
	}

	// Set file permissions to be secure (readable only by owner)
	if err := os.Chmod(mainLogFile, 0600); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("file", mainLogFile).Msg("Failed to set secure permissions on log file")
	}

	// Setup proxy logger
	proxyLogFile := filepath.Join(cfg.Logging.Dir, cfg.Logging.ProxyLogFile)
	proxyFileWriter := &lumberjack.Logger{
		Filename:   proxyLogFile,
		MaxSize:    cfg.Logging.MaxSize,
		MaxBackups: cfg.Logging.MaxBackups,
		MaxAge:     cfg.Logging.MaxAge,
		Compress:   cfg.Logging.Compress,
	}

	// Set file permissions to be secure (readable only by owner)
	if err := os.Chmod(proxyLogFile, 0600); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("file", proxyLogFile).Msg("Failed to set secure permissions on log file")
	}

	// Create multi-writers (console + file)
	consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr}
	
	mainMultiWriter := io.MultiWriter(consoleWriter, mainFileWriter)
	proxyMultiWriter := io.MultiWriter(consoleWriter, proxyFileWriter)

	// Initialize loggers
	MainLogger = zerolog.New(mainMultiWriter).With().Timestamp().Logger()
	ProxyLogger = zerolog.New(proxyMultiWriter).With().Timestamp().Logger()

	// Set the global logger to the main logger
	log.Logger = MainLogger

	log.Info().
		Str("main_log", mainLogFile).
		Str("proxy_log", proxyLogFile).
		Str("container_log_dir", containerLogDir).
		Str("level", level.String()).
		Msg("File logging initialized")

	return nil
}

// GetContainerLogWriter returns a log writer for a specific container
func GetContainerLogWriter(cfg *config.Config, containerID string, containerName string) (*lumberjack.Logger, error) {
	if !cfg.Logging.Enabled {
		return nil, fmt.Errorf("logging is disabled")
	}

	containerLogDir := filepath.Join(cfg.Logging.Dir, cfg.Logging.ContainerLogDir)
	
	// Ensure container log directory exists with secure permissions
	if err := os.MkdirAll(containerLogDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create container log directory: %w", err)
	}

	// Use container ID as the main log file name
	logFile := filepath.Join(containerLogDir, fmt.Sprintf("%s.log", containerID))
	
	writer := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    cfg.Logging.MaxSize,
		MaxBackups: cfg.Logging.MaxBackups,
		MaxAge:     cfg.Logging.MaxAge,
		Compress:   cfg.Logging.Compress,
	}

	// Set file permissions to be secure (readable only by owner)
	if err := os.Chmod(logFile, 0600); err != nil && !os.IsNotExist(err) {
		log.Warn().Err(err).Str("file", logFile).Msg("Failed to set secure permissions on container log file")
	}

	// Create a symlink for easier access using container name (if provided)
	if containerName != "" && containerName != containerID {
		symlinkPath := filepath.Join(containerLogDir, fmt.Sprintf("%s.log", containerName))
		
		// Remove existing symlink if it exists
		if _, err := os.Lstat(symlinkPath); err == nil {
			os.Remove(symlinkPath)
		}
		
		// Create new symlink
		relativeLogFile := fmt.Sprintf("%s.log", containerID)
		if err := os.Symlink(relativeLogFile, symlinkPath); err != nil {
			log.Warn().
				Err(err).
				Str("symlink", symlinkPath).
				Str("target", relativeLogFile).
				Msg("Failed to create container name symlink")
		}
	}

	return writer, nil
}

// Close gracefully closes all logging writers
func Close() {
	// Lumberjack loggers will be closed automatically when the program exits
	// This function is provided for future extensibility
}