package logger

import (
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/log"
)

// Logger is a wrapper around charmbracelet/log.Logger
type Logger struct {
	*log.Logger
}

var (
	instance *Logger
	once     sync.Once
)

// GetLogger returns the singleton logger instance
func GetLogger() *Logger {
	once.Do(func() {
		instance = &Logger{
			Logger: log.NewWithOptions(os.Stderr, log.Options{
				Level:          log.InfoLevel,
				ReportTimestamp: true,
				TimeFormat:     "15:04:05",
			}),
		}
	})
	return instance
}

// SetLogLevel sets the log level from a string
func (l *Logger) SetLogLevel(level string) {
	var logLevel log.Level
	switch strings.ToLower(level) {
	case "debug":
		logLevel = log.DebugLevel
	case "info":
		logLevel = log.InfoLevel
	case "warn", "warning":
		logLevel = log.WarnLevel
	case "error":
		logLevel = log.ErrorLevel
	case "fatal":
		logLevel = log.FatalLevel
	default:
		// Default to info level for unknown values
		logLevel = log.InfoLevel
	}

	l.SetLevel(logLevel)
	log.SetLevel(logLevel) // Set the global logger level too
	l.Debug("Log level set", "level", level)
}

// ConfigureFromEnv configures the logger from environment variables
func (l *Logger) ConfigureFromEnv() {
	// Check for GORDON_LOG_LEVEL environment variable
	if logLevelEnv := os.Getenv("GORDON_LOG_LEVEL"); logLevelEnv != "" {
		l.SetLogLevel(logLevelEnv)
		l.Debug("Log level set from environment variable", "level", logLevelEnv)
	} else if os.Getenv("ENV") == "dev" {
		// Fall back to ENV=dev behavior if GORDON_LOG_LEVEL is not set
		l.SetLevel(log.DebugLevel)
		log.SetLevel(log.DebugLevel) // Set the global logger level too
		l.Debug("Debug logging enabled from ENV=dev")
	}
}

// Debug logs a debug message
func Debug(msg string, keyvals ...interface{}) {
	GetLogger().Debug(msg, keyvals...)
}

// Info logs an info message
func Info(msg string, keyvals ...interface{}) {
	GetLogger().Info(msg, keyvals...)
}

// Warn logs a warning message
func Warn(msg string, keyvals ...interface{}) {
	GetLogger().Warn(msg, keyvals...)
}

// Error logs an error message
func Error(msg string, keyvals ...interface{}) {
	GetLogger().Error(msg, keyvals...)
}

// Fatal logs a fatal message and exits
func Fatal(msg string, keyvals ...interface{}) {
	GetLogger().Fatal(msg, keyvals...)
}

// Fatalf logs a fatal message with formatting and exits
func Fatalf(format string, args ...interface{}) {
	GetLogger().Fatalf(format, args...)
}
