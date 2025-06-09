package logging

import (
	"bufio"
	"context"
	"fmt"
	"sync"
	"time"

	"gordon/internal/config"
	"gordon/pkg/runtime"

	"github.com/rs/zerolog/log"
	"gopkg.in/natefinch/lumberjack.v2"
)

// LogCollector manages log collection for a single container
type LogCollector struct {
	containerID   string
	containerName string
	runtime       runtime.Runtime
	logWriter     *lumberjack.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

// LogManager manages all container log collectors
type LogManager struct {
	collectors map[string]*LogCollector
	config     *config.Config
	runtime    runtime.Runtime
	mu         sync.RWMutex
}

// NewLogManager creates a new log manager
func NewLogManager(cfg *config.Config, rt runtime.Runtime) *LogManager {
	return &LogManager{
		collectors: make(map[string]*LogCollector),
		config:     cfg,
		runtime:    rt,
	}
}

// StartCollection starts log collection for a container
func (lm *LogManager) StartCollection(containerID, containerName string) error {
	if !lm.config.Logging.Enabled {
		return nil // Logging disabled, nothing to do
	}

	lm.mu.Lock()
	defer lm.mu.Unlock()

	// Stop existing collector if it exists
	if existing, exists := lm.collectors[containerID]; exists {
		log.Debug().Str("container_id", containerID).Msg("Stopping existing log collector")
		existing.Stop()
		delete(lm.collectors, containerID)
	}

	// Create log writer for this container
	logWriter, err := GetContainerLogWriter(lm.config, containerID, containerName)
	if err != nil {
		return fmt.Errorf("failed to create log writer for container %s: %w", containerID, err)
	}

	// Create collector context
	ctx, cancel := context.WithCancel(context.Background())

	collector := &LogCollector{
		containerID:   containerID,
		containerName: containerName,
		runtime:       lm.runtime,
		logWriter:     logWriter,
		ctx:           ctx,
		cancel:        cancel,
	}

	// Start collection goroutine
	collector.wg.Add(1)
	go collector.collectLogs()

	// Store collector
	lm.collectors[containerID] = collector

	log.Info().
		Str("container_id", containerID).
		Str("container_name", containerName).
		Msg("Started container log collection")

	return nil
}

// StopCollection stops log collection for a container
func (lm *LogManager) StopCollection(containerID string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if collector, exists := lm.collectors[containerID]; exists {
		log.Debug().Str("container_id", containerID).Msg("Stopping log collection")
		collector.Stop()
		delete(lm.collectors, containerID)
	}
}

// StopAll stops all log collection
func (lm *LogManager) StopAll() {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	for containerID, collector := range lm.collectors {
		log.Debug().Str("container_id", containerID).Msg("Stopping log collection")
		collector.Stop()
	}
	lm.collectors = make(map[string]*LogCollector)
}

// GetActiveCollectors returns the number of active collectors
func (lm *LogManager) GetActiveCollectors() int {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return len(lm.collectors)
}

// collectLogs runs the log collection loop for a single container
func (lc *LogCollector) collectLogs() {
	defer lc.wg.Done()

	log.Debug().
		Str("container_id", lc.containerID).
		Str("container_name", lc.containerName).
		Msg("Starting log collection goroutine")

	// Retry loop for log collection
	retryDelay := time.Second
	maxRetryDelay := 30 * time.Second

	for {
		select {
		case <-lc.ctx.Done():
			log.Debug().Str("container_id", lc.containerID).Msg("Log collection context cancelled")
			return
		default:
			// Attempt to collect logs
			if err := lc.collectLogsOnce(); err != nil {
				log.Warn().
					Err(err).
					Str("container_id", lc.containerID).
					Dur("retry_delay", retryDelay).
					Msg("Log collection failed, retrying")

				// Wait before retrying, with exponential backoff
				select {
				case <-lc.ctx.Done():
					return
				case <-time.After(retryDelay):
					// Increase retry delay, up to maximum
					retryDelay *= 2
					if retryDelay > maxRetryDelay {
						retryDelay = maxRetryDelay
					}
				}
				continue
			}

			// Reset retry delay on success
			retryDelay = time.Second
		}
	}
}

// collectLogsOnce attempts to collect logs once
func (lc *LogCollector) collectLogsOnce() error {
	// Get log stream from runtime
	logStream, err := lc.runtime.GetContainerLogs(lc.ctx, lc.containerID, true)
	if err != nil {
		return fmt.Errorf("failed to get log stream: %w", err)
	}
	defer logStream.Close()

	log.Debug().Str("container_id", lc.containerID).Msg("Started reading container logs")

	// Read logs line by line and write to file
	scanner := bufio.NewScanner(logStream)
	for scanner.Scan() {
		select {
		case <-lc.ctx.Done():
			return nil
		default:
			// Get log line
			line := scanner.Text()

			// Add timestamp and write to log file
			timestampedLine := fmt.Sprintf("%s %s\n",
				time.Now().Format(time.RFC3339),
				line)

			if _, err := lc.logWriter.Write([]byte(timestampedLine)); err != nil {
				log.Warn().
					Err(err).
					Str("container_id", lc.containerID).
					Msg("Failed to write container log line")
			}
		}
	}

	// Check for scanner errors
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading log stream: %w", err)
	}

	return nil
}

// Stop gracefully stops the log collector
func (lc *LogCollector) Stop() {
	log.Debug().Str("container_id", lc.containerID).Msg("Stopping log collector")

	// Cancel context to stop goroutines
	lc.cancel()

	// Wait for collection goroutine to finish
	lc.wg.Wait()

	// Close log writer
	if lc.logWriter != nil {
		if err := lc.logWriter.Close(); err != nil {
			log.Warn().
				Err(err).
				Str("container_id", lc.containerID).
				Msg("Failed to close log writer")
		}
	}

	log.Debug().Str("container_id", lc.containerID).Msg("Log collector stopped")
}
