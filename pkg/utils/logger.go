package utils

import (
	"os"
	"sync"

	"github.com/rs/zerolog"
)

const (
	App LoggerType = iota
	APP
	HTTP
	Database
	Admin
	User
	Auth
	Utils
	Render
)

type LoggerType int
type Logger struct {
	zerolog.Logger
}
type LogFileManager struct {
	files map[LoggerType]*os.File
	mu    sync.Mutex
}

func NewLogger() *Logger {
	writer := zerolog.ConsoleWriter{Out: os.Stdout} // changed to os.Stdout

	// Create a new logger
	zl := zerolog.New(writer).With().Timestamp().Logger()
	return &Logger{zl}
}

func NewLogFileManager() *LogFileManager {
	return &LogFileManager{
		files: make(map[LoggerType]*os.File),
	}
}

// GetTypeLogger returns a logger with the specified type
func (l *Logger) GetTypeLogger(loggerType LoggerType) *Logger {
	switch loggerType {
	case App:
		return &Logger{l.With().Str("type", "app").Logger()}
	case HTTP:
		return &Logger{l.With().Str("type", "http").Logger()}
	case Database:
		return &Logger{l.With().Str("type", "database").Logger()}
	case Admin:
		return &Logger{l.With().Str("type", "admin").Logger()}
	case User:
		return &Logger{l.With().Str("type", "user").Logger()}
	case Auth:
		return &Logger{l.With().Str("type", "auth").Logger()}
	default:
		return &Logger{l.With().Str("type", "undefined").Logger()}
	}
}

// GetTypeLogger returns a logger with the specified type
func (m *LogFileManager) OpenLogFile(loggerType LoggerType) (*os.File, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if file, exists := m.files[loggerType]; exists {
		return file, nil
	}

	logFilePath := GetLogFileForType(loggerType)
	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	m.files[loggerType] = logFile
	return logFile, nil
}

// CreateLogsDir creates the logs directory if it doesn't exist
func CreateLogsDir() error {
	if _, err := os.Stat("logs"); os.IsNotExist(err) {
		err := os.Mkdir("logs", 0755)
		if err != nil {
			return err
		}
	}
	return nil
}

// GetLogFileForType returns the log file for the specified logger type
func GetLogFileForType(loggerType LoggerType) string {
	switch loggerType {
	case App, Database, Utils, Render:
		return "logs/app.log"
	case HTTP, Admin, User, Auth:
		return "logs/http.log"
	default:
		return "logs/undefined.log"
	}
}

// CreateLogFile creates a log file for each logger type
func CreateLogFile(loggerType LoggerType) (*os.File, error) {
	logFilePath := GetLogFileForType(loggerType)
	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	return logFile, nil
}

// GetLogLevel returns the log level for the specified logger type
func GetLogLevel(loggerType LoggerType) zerolog.Level {
	switch loggerType {
	case App, Database, Utils, Render:
		return zerolog.InfoLevel
	case HTTP, Admin, User, Auth:
		return zerolog.DebugLevel
	default:
		return zerolog.DebugLevel
	}
}

// CloseLogFile closes the log file for the specified logger type
func (m *LogFileManager) CloseLogFile(loggerType LoggerType) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if file, exists := m.files[loggerType]; exists {
		err := file.Close()
		if err != nil {
			return err
		}
		delete(m.files, loggerType)
	}
	return nil
}
