// Package accesslog provides a dedicated HTTP access log writer for security
// and observability tooling. It supports JSON, CLF, and Combined formats with
// stdout, file, and journald output backends.
package accesslog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Config holds configuration for the access log writer.
type Config struct {
	// Format is the output format: "json", "clf", or "combined".
	Format string
	// Output is the output backend: "stdout", "file", or "journald".
	Output string
	// FilePath is the path to the log file (only used when Output == "file").
	// If empty, defaults to {data_dir}/logs/access.log (caller must resolve).
	FilePath string
	// MaxSize is the maximum size in MB before rotation (only used when Output == "file").
	MaxSize int
	// MaxBackups is the number of old log files to retain (only used when Output == "file").
	MaxBackups int
	// MaxAge is the maximum number of days to retain old log files (only used when Output == "file").
	MaxAge int
	// SyslogIdentifier is the journald SYSLOG_IDENTIFIER (only used when Output == "journald").
	SyslogIdentifier string
}

// Entry holds the data for a single HTTP access log record.
type Entry struct {
	Time       time.Time
	ClientIP   string
	Method     string
	Host       string
	Path       string
	Query      string
	Status     int
	BytesSent  int
	DurationMS float64
	UserAgent  string
	Referer    string
	RequestID  string
	Proto      string
}

// sink is the internal interface for output backends.
type sink interface {
	WriteLine(line string) error
	Close() error
}

// Writer serializes access log entries and writes them to a configured sink.
// It is safe for concurrent use.
type Writer struct {
	formatter func(Entry) (string, error)
	sink      sink
	mu        sync.Mutex
}

// New creates a new Writer from cfg. Returns an error if the configuration is
// invalid or the output backend cannot be initialized.
func New(cfg Config) (*Writer, error) {
	formatter, err := newFormatter(cfg.Format)
	if err != nil {
		return nil, err
	}

	s, err := newSink(cfg)
	if err != nil {
		return nil, err
	}

	return &Writer{
		formatter: formatter,
		sink:      s,
	}, nil
}

// Write serializes entry and writes it to the configured output. It is safe
// for concurrent use. A write failure is returned as an error; callers should
// log the error and continue — Write never panics.
func (w *Writer) Write(entry Entry) error {
	line, err := w.formatter(entry)
	if err != nil {
		return fmt.Errorf("accesslog: format entry: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.sink.WriteLine(line); err != nil {
		return fmt.Errorf("accesslog: write line: %w", err)
	}
	return nil
}

// Close releases any resources held by the writer (e.g. open file handles).
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sink.Close()
}

// newFormatter returns the format function for the given format name.
func newFormatter(format string) (func(Entry) (string, error), error) {
	switch format {
	case "json":
		return formatJSON, nil
	case "clf":
		return formatCLF, nil
	case "combined":
		return formatCombined, nil
	default:
		return nil, fmt.Errorf("invalid logging.access_log.format: %q (must be json, clf, or combined)", format)
	}
}

// newSink creates the output sink for the given config.
func newSink(cfg Config) (sink, error) {
	switch cfg.Output {
	case "stdout", "":
		return &stdoutSink{}, nil
	case "file":
		return newFileSink(cfg)
	case "journald":
		return newJournaldSink(cfg)
	default:
		return nil, fmt.Errorf("invalid logging.access_log.output: %q (must be stdout, file, or journald)", cfg.Output)
	}
}

// stdoutSink writes lines to os.Stdout.
type stdoutSink struct{}

func (s *stdoutSink) WriteLine(line string) error {
	_, err := fmt.Fprintln(os.Stdout, line)
	return err
}

func (s *stdoutSink) Close() error { return nil }

// fileSink writes lines to a rotating file via lumberjack.
type fileSink struct {
	logger *lumberjack.Logger
}

func newFileSink(cfg Config) (*fileSink, error) {
	if cfg.FilePath == "" {
		return nil, fmt.Errorf("accesslog: file output requires a non-empty file_path")
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0o700); err != nil {
		return nil, fmt.Errorf("accesslog: create log directory: %w", err)
	}

	// lumberjack defaults when zero: MaxSize=100MB, MaxBackups=keep all, MaxAge=no limit.
	// Config defaults in run.go set these to sensible values; zero here is valid.
	return &fileSink{
		logger: &lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    cfg.MaxSize,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAge,
			Compress:   true,
		},
	}, nil
}

func (s *fileSink) WriteLine(line string) error {
	_, err := fmt.Fprintln(s.logger, line)
	return err
}

func (s *fileSink) Close() error {
	return s.logger.Close()
}
