// Package app provides the application initialization and wiring.
package app

import (
	"fmt"
	"path/filepath"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/adapters/out/accesslog"
)

// initLogger initializes the zerowrap logger.
func initLogger(cfg Config) (zerowrap.Logger, func(), error) {
	logConfig := zerowrap.Config{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
	}

	// Always enable file logging so admin API can read process logs
	// (Config flag is respected for backward-compatibility)
	logPath := cfg.Logging.File.Path
	if logPath == "" {
		dataDir := cfg.Server.DataDir
		if dataDir == "" {
			dataDir = DefaultDataDir()
		}
		logPath = filepath.Join(dataDir, "logs", "gordon.log")
	}

	log, cleanup, err := zerowrap.NewWithFile(logConfig, zerowrap.FileConfig{
		Enabled:    cfg.Logging.File.Enabled,
		Path:       logPath,
		MaxSize:    cfg.Logging.File.MaxSize,
		MaxBackups: cfg.Logging.File.MaxBackups,
		MaxAge:     cfg.Logging.File.MaxAge,
		Compress:   true,
	})
	if err != nil {
		return zerowrap.Default(), nil, fmt.Errorf("failed to create logger with file: %w", err)
	}
	return log, cleanup, nil
}

// initAccessLog creates an access log writer when access logging is enabled.
// Returns nil, nil when disabled — callers must treat nil writer as "disabled".
func initAccessLog(cfg Config, log zerowrap.Logger) (*accesslog.Writer, error) {
	if !cfg.Logging.AccessLog.Enabled {
		return nil, nil
	}

	filePath := cfg.Logging.AccessLog.FilePath
	if filePath == "" && cfg.Logging.AccessLog.Output == "file" {
		dataDir := cfg.Server.DataDir
		if dataDir == "" {
			dataDir = DefaultDataDir()
		}
		filePath = filepath.Join(dataDir, "logs", "access.log")
	}

	writer, err := accesslog.New(accesslog.Config{
		Format:           cfg.Logging.AccessLog.Format,
		Output:           cfg.Logging.AccessLog.Output,
		FilePath:         filePath,
		MaxSize:          cfg.Logging.AccessLog.MaxSize,
		MaxBackups:       cfg.Logging.AccessLog.MaxBackups,
		MaxAge:           cfg.Logging.AccessLog.MaxAge,
		SyslogIdentifier: cfg.Logging.AccessLog.SyslogIdentifier,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize access log: %w", err)
	}

	log.Info().
		Str("format", cfg.Logging.AccessLog.Format).
		Str("output", cfg.Logging.AccessLog.Output).
		Msg("access log enabled")

	return writer, nil
}
