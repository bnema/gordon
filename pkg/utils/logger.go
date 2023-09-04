package utils

import (
	"fmt"
	"io"
	"os"

	"github.com/labstack/gommon/log"
	"github.com/rs/zerolog"
)

type LogLevel string

const (
	DEBUG LogLevel = "debug"
	INFO  LogLevel = "info"
	WARN  LogLevel = "warn"
	ERROR LogLevel = "error"
	FATAL LogLevel = "fatal"
	PANIC LogLevel = "panic"
)

type Logger struct {
	zerolog.Logger
}

type EchoLoggerWrapper struct {
	*Logger
	output io.Writer
}

// Map log levels to zerolog log functions
func (l *Logger) getLogFunc(level LogLevel) LogFunc {
	switch level {
	case DEBUG:
		return func() *zerolog.Event { return l.Logger.Debug() }
	case INFO:
		return func() *zerolog.Event { return l.Logger.Info() }
	case WARN:
		return func() *zerolog.Event { return l.Logger.Warn() }
	case ERROR:
		return func() *zerolog.Event { return l.Logger.Error() }
	case FATAL:
		return func() *zerolog.Event { return l.Logger.Fatal() }
	case PANIC:
		return func() *zerolog.Event { return l.Logger.Panic() }
	default:
		return func() *zerolog.Event { return l.Logger.Info() } // default to INFO if unknown level
	}
}

// LogFunc is a function that logs a message with optional context.
type LogFunc func() *zerolog.Event

func (l *Logger) Log(level LogLevel, i ...interface{}) {
	logFunc := l.getLogFunc(level)
	logFunc().Msg(fmt.Sprint(i...))
}

func NewLogger() *Logger {
	writer := zerolog.ConsoleWriter{Out: os.Stdout}
	zl := zerolog.New(writer).With().Timestamp().Logger()
	return &Logger{zl}
}

func NewEchoLoggerWrapper(l *Logger) *EchoLoggerWrapper {
	return &EchoLoggerWrapper{Logger: l, output: os.Stdout}
}

func (l *Logger) SetOutput(w io.Writer) *Logger {
	l.Logger = l.Logger.Output(w)
	return l
}

func (l *Logger) SetLevel(level LogLevel) *Logger {
	l.Logger = l.Logger.Level(getZerologLevel(level))
	return l
}

func getZerologLevel(level LogLevel) zerolog.Level {
	switch level {
	case DEBUG:
		return zerolog.DebugLevel
	case INFO:
		return zerolog.InfoLevel
	case WARN:
		return zerolog.WarnLevel
	case ERROR:
		return zerolog.ErrorLevel
	case FATAL:
		return zerolog.FatalLevel
	case PANIC:
		return zerolog.PanicLevel
	default:
		return zerolog.InfoLevel // default to INFO if unknown level
	}
}

func CreateLogsDir() error {
	if _, err := os.Stat("logs"); os.IsNotExist(err) {
		err := os.Mkdir("logs", 0755)
		if err != nil {
			return err
		}
	}
	return nil
}

func (l *EchoLoggerWrapper) Output() io.Writer {
	return l.output
}

func (l *EchoLoggerWrapper) SetOutput(w io.Writer) {
	l.output = w
}

func (l *EchoLoggerWrapper) Prefix() string {
	return ""
}

func (l *EchoLoggerWrapper) SetPrefix(p string) {
	// do nothing
}

func (l *EchoLoggerWrapper) Level() log.Lvl {
	return log.Lvl(l.Logger.GetLevel())
}

// SetHeader
func (l *EchoLoggerWrapper) SetHeader(h string) {
	// do nothing
}

func (l *EchoLoggerWrapper) SetLevel(log.Lvl) {
	// do nothing
}

// SetEvent creates a new event with the given level and message.
func (l *EchoLoggerWrapper) SetEvent(level zerolog.Level, msg string) *zerolog.Event {
	//cannot use utils.NewEchoLoggerWrapper(app.HTTPLogger) (value of type *utils.EchoLoggerWrapper) as echo.Logger value in assignment: *utils.EchoLoggerWrapper does not implement echo.Logger (wrong type for method Error)
	return l.Logger.WithLevel(level).Str("message", msg)
}

func (l *EchoLoggerWrapper) OutputStdout() bool {
	return true
}

// Helper function for methods that accept variadic slice of interfaces
func (l *EchoLoggerWrapper) LogWithArgs(level LogLevel, i ...interface{}) {
	l.Logger.Log(level, i...)
}

// Helper function for methods that accept a format string and arguments
func (l *EchoLoggerWrapper) LogWithFormat(level LogLevel, format string, args ...interface{}) {
	l.Logger.Log(level, fmt.Sprintf(format, args...))
}

// Helper function for methods that accept a log.JSON object
func (l *EchoLoggerWrapper) LogWithJSON(level LogLevel, j log.JSON) {
	l.Logger.Log(level, j)
}

// Debug
func (l *EchoLoggerWrapper) Debug(i ...interface{}) {
	l.LogWithArgs(DEBUG, i...)
}

func (l *EchoLoggerWrapper) Debugf(format string, args ...interface{}) {
	l.LogWithFormat(DEBUG, format, args...)
}

func (l *EchoLoggerWrapper) Debugj(j log.JSON) {
	l.LogWithJSON(DEBUG, j)
}

// Info
func (l *EchoLoggerWrapper) Info(i ...interface{}) {
	l.LogWithArgs(INFO, i...)
}

func (l *EchoLoggerWrapper) Infof(format string, args ...interface{}) {
	l.LogWithFormat(INFO, format, args...)
}

func (l *EchoLoggerWrapper) Infoj(j log.JSON) {
	l.LogWithJSON(INFO, j)
}

// Warn

func (l *EchoLoggerWrapper) Warn(i ...interface{}) {
	l.LogWithArgs(WARN, i...)
}

func (l *EchoLoggerWrapper) Warnf(format string, args ...interface{}) {
	l.LogWithFormat(WARN, format, args...)
}

func (l *EchoLoggerWrapper) Warnj(j log.JSON) {
	l.LogWithJSON(WARN, j)
}

// Error
func (l *EchoLoggerWrapper) Error(i ...interface{}) {
	l.LogWithArgs(ERROR, i...)
}

func (l *EchoLoggerWrapper) Errorf(format string, args ...interface{}) {
	l.LogWithFormat(ERROR, format, args...)
}

func (l *EchoLoggerWrapper) Errorj(j log.JSON) {
	l.LogWithJSON(ERROR, j)
}

// Fatal
func (l *EchoLoggerWrapper) Fatal(i ...interface{}) {
	l.LogWithArgs(FATAL, i...)
}

func (l *EchoLoggerWrapper) Fatalf(format string, args ...interface{}) {
	l.LogWithFormat(FATAL, format, args...)
}

func (l *EchoLoggerWrapper) Fatalj(j log.JSON) {
	l.LogWithJSON(FATAL, j)
}

// Panic

func (l *EchoLoggerWrapper) Panic(i ...interface{}) {
	l.LogWithArgs(PANIC, i...)
}

func (l *EchoLoggerWrapper) Panicf(format string, args ...interface{}) {
	l.LogWithFormat(PANIC, format, args...)
}

func (l *EchoLoggerWrapper) Panicj(j log.JSON) {
	l.LogWithJSON(PANIC, j)
}

// Print

func (l *EchoLoggerWrapper) Print(i ...interface{}) {
	l.LogWithArgs(INFO, i...)
}

func (l *EchoLoggerWrapper) Printf(format string, args ...interface{}) {
	l.LogWithFormat(INFO, format, args...)
}

func (l *EchoLoggerWrapper) Printj(j log.JSON) {
	l.LogWithJSON(INFO, j)
}
