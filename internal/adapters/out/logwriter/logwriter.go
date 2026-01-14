// Package logwriter provides container log collection with file rotation.
package logwriter

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bnema/zerowrap"
	"github.com/docker/docker/pkg/stdcopy"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Config holds the configuration for the log writer.
type Config struct {
	// Dir is the directory where container logs are stored.
	Dir string
	// MaxSize is the maximum size in megabytes before rotation.
	MaxSize int
	// MaxBackups is the number of old log files to retain.
	MaxBackups int
	// MaxAge is the maximum number of days to retain old log files.
	MaxAge int
}

// LogWriter implements the ContainerLogWriter interface.
type LogWriter struct {
	config  Config
	streams map[string]*streamInfo
	mu      sync.RWMutex
}

// streamInfo tracks an active log stream for a container.
type streamInfo struct {
	containerID string
	domain      string
	logStream   io.ReadCloser
	logger      *lumberjack.Logger
	cancel      context.CancelFunc
	done        chan struct{}
}

// New creates a new LogWriter.
func New(config Config) (*LogWriter, error) {
	// Ensure the log directory exists
	if err := os.MkdirAll(config.Dir, 0700); err != nil {
		return nil, err
	}

	return &LogWriter{
		config:  config,
		streams: make(map[string]*streamInfo),
	}, nil
}

// StartLogging begins collecting logs for a container.
func (w *LogWriter) StartLogging(ctx context.Context, containerID string, domain string, logStream io.ReadCloser) error {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:    "adapter",
		zerowrap.FieldAdapter:  "logwriter",
		zerowrap.FieldAction:   "StartLogging",
		zerowrap.FieldEntityID: containerID,
		"domain":               domain,
	})
	log := zerowrap.FromCtx(ctx)

	w.mu.Lock()
	defer w.mu.Unlock()

	// Stop existing logging for this container if any
	if existing, ok := w.streams[containerID]; ok {
		log.Debug().Msg("stopping existing log stream before starting new one")
		w.stopStreamLocked(existing)
	}

	// Sanitize domain for filename
	filename := sanitizeDomain(domain) + ".log"
	logPath := filepath.Join(w.config.Dir, filename)

	// Create lumberjack logger for rotation
	logger := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    w.config.MaxSize,
		MaxBackups: w.config.MaxBackups,
		MaxAge:     w.config.MaxAge,
		Compress:   true,
	}

	// Create cancellable context for the streaming goroutine
	streamCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	info := &streamInfo{
		containerID: containerID,
		domain:      domain,
		logStream:   logStream,
		logger:      logger,
		cancel:      cancel,
		done:        done,
	}

	w.streams[containerID] = info

	// Start streaming goroutine
	go w.streamLogs(streamCtx, info, log)

	log.Info().Str("path", logPath).Msg("started container log collection")
	return nil
}

// StopLogging stops log collection for a container.
func (w *LogWriter) StopLogging(containerID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if info, ok := w.streams[containerID]; ok {
		w.stopStreamLocked(info)
		delete(w.streams, containerID)
	}
	return nil
}

// Close stops all logging and releases resources.
func (w *LogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	for containerID, info := range w.streams {
		w.stopStreamLocked(info)
		delete(w.streams, containerID)
	}
	return nil
}

// stopStreamLocked stops a stream (must be called with lock held).
func (w *LogWriter) stopStreamLocked(info *streamInfo) {
	// Signal the goroutine to stop
	info.cancel()

	// Close the log stream to unblock reads
	info.logStream.Close()

	// Wait for goroutine to finish
	<-info.done

	// Close the lumberjack logger
	info.logger.Close()
}

// streamLogs copies from the Docker log stream to the file.
func (w *LogWriter) streamLogs(ctx context.Context, info *streamInfo, log zerowrap.Logger) {
	defer close(info.done)

	// Docker container logs are multiplexed with 8-byte headers.
	// stdcopy.StdCopy demultiplexes them to separate stdout/stderr writers.
	// We combine both to a single file.
	_, err := stdcopy.StdCopy(info.logger, info.logger, info.logStream)

	if err != nil && ctx.Err() == nil {
		// Only log error if we weren't cancelled
		log.Warn().Err(err).
			Str("container_id", info.containerID).
			Str("domain", info.domain).
			Msg("container log streaming ended with error")
	}
}

// sanitizeDomain converts a domain to a safe filename.
func sanitizeDomain(domain string) string {
	// Replace dots with underscores, remove other unsafe characters
	safe := strings.ReplaceAll(domain, ".", "_")
	safe = strings.ReplaceAll(safe, "/", "_")
	safe = strings.ReplaceAll(safe, ":", "_")
	safe = strings.ReplaceAll(safe, " ", "_")
	return safe
}
