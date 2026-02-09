// Package logs implements the log access use case.
package logs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/in"
	"github.com/bnema/gordon/internal/boundaries/out"
)

// Service implements the LogService interface.
type Service struct {
	logFilePath        string
	fileLoggingEnabled bool
	containerSvc       in.ContainerService
	runtime            out.ContainerRuntime
	log                zerowrap.Logger
}

var execCommandContext = exec.CommandContext

// NewService creates a new log service.
func NewService(
	logFilePath string,
	fileLoggingEnabled bool,
	containerSvc in.ContainerService,
	runtime out.ContainerRuntime,
	log zerowrap.Logger,
) *Service {
	return &Service{
		logFilePath:        logFilePath,
		fileLoggingEnabled: fileLoggingEnabled,
		containerSvc:       containerSvc,
		runtime:            runtime,
		log:                log,
	}
}

// GetProcessLogs returns the last N lines of Gordon process logs.
func (s *Service) GetProcessLogs(ctx context.Context, lines int) ([]string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetProcessLogs",
		"lines":               lines,
	})
	log := zerowrap.FromCtx(ctx)

	if s.useFileProcessLogs() {
		if s.logFilePath == "" {
			return nil, fmt.Errorf("log file path not configured")
		}

		file, err := os.Open(s.logFilePath)
		if err != nil {
			if os.IsNotExist(err) {
				log.Warn().Str("log_path", s.logFilePath).
					Msg("process log file missing, falling back to journalctl")
				return s.getProcessLogsFromJournal(ctx, lines)
			}
			return nil, log.WrapErr(err, "failed to open log file")
		}
		defer file.Close()

		return tailLines(file, lines)
	}

	return s.getProcessLogsFromJournal(ctx, lines)
}

// FollowProcessLogs returns a channel that streams Gordon process log lines.
func (s *Service) FollowProcessLogs(ctx context.Context, initialLines int) (<-chan string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "FollowProcessLogs",
		"initial_lines":       initialLines,
	})
	if !s.useFileProcessLogs() {
		return s.followProcessLogsFromJournal(ctx, initialLines)
	}

	return s.followProcessLogsFromFileOrJournal(ctx, initialLines)
}

func (s *Service) useFileProcessLogs() bool {
	return s.fileLoggingEnabled
}

func (s *Service) followProcessLogsFromFileOrJournal(
	ctx context.Context,
	initialLines int,
) (<-chan string, error) {
	log := zerowrap.FromCtx(ctx)

	file, err := s.openProcessLogFile()
	if err != nil {
		if os.IsNotExist(err) {
			log.Warn().Str("log_path", s.logFilePath).
				Msg("process log file missing, falling back to journalctl follow")
			return s.followProcessLogsFromJournal(ctx, initialLines)
		}
		return nil, log.WrapErr(err, "failed to open log file")
	}

	return s.followProcessLogsFromFile(ctx, file, initialLines), nil
}

func (s *Service) openProcessLogFile() (*os.File, error) {
	if s.logFilePath == "" {
		return nil, fmt.Errorf("log file path not configured")
	}

	return os.Open(s.logFilePath)
}

func (s *Service) followProcessLogsFromFile(
	ctx context.Context,
	file *os.File,
	initialLines int,
) <-chan string {
	log := zerowrap.FromCtx(ctx)
	ch := make(chan string, 100)

	go func() {
		defer close(ch)
		defer file.Close()

		if !s.emitInitialFileLogs(ctx, file, initialLines, ch) {
			return
		}
		if err := s.seekToEndForFileFollow(file); err != nil {
			log.Warn().Err(err).Msg("failed to seek to end")
			return
		}

		s.streamFileFollowLogs(ctx, file, ch)
	}()

	return ch
}

func (s *Service) emitInitialFileLogs(
	ctx context.Context,
	file *os.File,
	initialLines int,
	ch chan<- string,
) bool {
	if initialLines <= 0 {
		return true
	}

	log := zerowrap.FromCtx(ctx)
	lines, err := tailLines(file, initialLines)
	if err != nil {
		log.Warn().Err(err).Msg("failed to read initial lines")
		return true
	}

	for _, line := range lines {
		if !sendLogLineWithChannel(ctx, ch, line) {
			return false
		}
	}

	return true
}

func (s *Service) seekToEndForFileFollow(file *os.File) error {
	_, err := file.Seek(0, io.SeekEnd)
	return err
}

func (s *Service) streamFileFollowLogs(ctx context.Context, file *os.File, ch chan<- string) {
	log := zerowrap.FromCtx(ctx)
	reader := bufio.NewReader(file)

	for {
		if ctx.Err() != nil {
			return
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			log.Warn().Err(err).Msg("error reading log file")
			return
		}

		line = strings.TrimRight(line, "\n\r")
		if !sendLogLineWithChannel(ctx, ch, line) {
			return
		}
	}
}

func sendLogLineWithChannel(ctx context.Context, ch chan<- string, line string) bool {
	select {
	case ch <- line:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) getProcessLogsFromJournal(ctx context.Context, lines int) ([]string, error) {
	userScope, err := resolveJournalctlScope(ctx)
	if err != nil {
		return nil, err
	}

	args := []string{"-u", "gordon", "-n", strconv.Itoa(lines), "--no-pager", "-o", "cat"}
	if userScope {
		args = append([]string{"--user"}, args...)
	}

	out, err := execCommandContext(ctx, "journalctl", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("failed to read journalctl logs: %w", err)
	}

	return outputLines(out), nil
}

func (s *Service) followProcessLogsFromJournal(ctx context.Context, initialLines int) (<-chan string, error) {
	userScope, err := resolveJournalctlScope(ctx)
	if err != nil {
		return nil, err
	}

	args := []string{
		"-u", "gordon",
		"-n", strconv.Itoa(initialLines),
		"--no-pager",
		"-o", "cat",
		"-f",
	}
	if userScope {
		args = append([]string{"--user"}, args...)
	}

	cmd := execCommandContext(ctx, "journalctl", args...) // #nosec G204 -- internal args only
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open journalctl stdout: %w", err)
	}
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start journalctl follow: %w", err)
	}

	ch := make(chan string, 100)

	go func() {
		defer close(ch)
		defer stdout.Close()
		defer cmd.Wait() //nolint:errcheck // best-effort cleanup

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			select {
			case ch <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

func resolveJournalctlScope(ctx context.Context) (bool, error) {
	userLogs, userErr := hasJournalctlLogs(ctx, true)
	if userErr == nil && userLogs {
		return true, nil
	}

	systemLogs, systemErr := hasJournalctlLogs(ctx, false)
	if systemErr == nil && systemLogs {
		return false, nil
	}

	if userErr != nil && systemErr != nil {
		return false, fmt.Errorf("journalctl unavailable for user and system services")
	}

	// Default to user scope when logs are currently empty; this matches rootless setups.
	return true, nil
}

func hasJournalctlLogs(ctx context.Context, userService bool) (bool, error) {
	args := []string{"-u", "gordon", "-n", "1", "--no-pager", "-q", "-o", "cat"}
	if userService {
		args = append([]string{"--user"}, args...)
	}

	out, err := execCommandContext(ctx, "journalctl", args...).Output()
	if err != nil {
		return false, err
	}

	return len(bytes.TrimSpace(out)) > 0, nil
}

func outputLines(out []byte) []string {
	trimmed := strings.TrimRight(string(out), "\n\r")
	if trimmed == "" {
		return []string{}
	}
	return strings.Split(trimmed, "\n")
}

// validateDomain checks that domain is safe for use in logs and error messages.
func validateDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("domain cannot be empty")
	}
	if len(domain) > 253 {
		return fmt.Errorf("domain too long")
	}
	if strings.Contains(domain, "..") {
		return fmt.Errorf("invalid domain")
	}
	if strings.ContainsAny(domain, "\x00\n\r") {
		return fmt.Errorf("invalid domain")
	}
	return nil
}

// GetContainerLogs returns the last N lines of container logs for a domain.
func (s *Service) GetContainerLogs(ctx context.Context, domain string, lines int) ([]string, error) {
	if err := validateDomain(domain); err != nil {
		return nil, err
	}

	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "GetContainerLogs",
		"domain":              domain,
		"lines":               lines,
	})
	log := zerowrap.FromCtx(ctx)

	// Get container by domain
	container, ok := s.containerSvc.Get(ctx, domain)
	if !ok || container == nil {
		return nil, fmt.Errorf("container not found for domain: %s", domain)
	}

	// Get logs from container runtime (non-follow mode)
	reader, err := s.runtime.GetContainerLogs(ctx, container.ID, false)
	if err != nil {
		return nil, log.WrapErr(err, "failed to get container logs")
	}
	defer reader.Close()

	// Parse Docker log stream and get lines
	logLines, err := parseDockerLogs(reader)
	if err != nil {
		return nil, log.WrapErr(err, "failed to parse container logs")
	}

	// Return last N lines
	if len(logLines) > lines {
		logLines = logLines[len(logLines)-lines:]
	}

	return logLines, nil
}

// FollowContainerLogs returns a channel that streams container log lines.
func (s *Service) FollowContainerLogs(ctx context.Context, domain string, initialLines int) (<-chan string, error) {
	if err := validateDomain(domain); err != nil {
		return nil, err
	}

	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "FollowContainerLogs",
		"domain":              domain,
		"initial_lines":       initialLines,
	})
	log := zerowrap.FromCtx(ctx)

	// Get container by domain
	container, ok := s.containerSvc.Get(ctx, domain)
	if !ok || container == nil {
		return nil, fmt.Errorf("container not found for domain: %s", domain)
	}

	// Get logs from container runtime (follow mode)
	reader, err := s.runtime.GetContainerLogs(ctx, container.ID, true)
	if err != nil {
		return nil, log.WrapErr(err, "failed to get container logs")
	}

	ch := make(chan string, 100)

	go func() {
		defer close(ch)
		defer reader.Close()

		// Stream Docker logs
		err := streamDockerLogs(ctx, reader, ch)
		if err != nil && ctx.Err() == nil {
			log.Warn().Err(err).Msg("error streaming container logs")
		}
	}()

	return ch, nil
}

// tailLines reads the last N lines from a file using a ring buffer.
func tailLines(file *os.File, n int) ([]string, error) {
	if n <= 0 {
		return []string{}, nil
	}

	// Seek to beginning
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(file)
	buffer := make([]string, n)
	index := 0
	total := 0

	for scanner.Scan() {
		buffer[index] = scanner.Text()
		index = (index + 1) % n
		total++
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if total == 0 {
		return []string{}, nil
	}

	// Build result in correct order
	var result []string
	if total <= n {
		result = make([]string, total)
		for i := 0; i < total; i++ {
			result[i] = buffer[i]
		}
	} else {
		result = make([]string, n)
		for i := 0; i < n; i++ {
			result[i] = buffer[(index+i)%n]
		}
	}

	return result, nil
}

// parseDockerLogs parses Docker multiplexed log stream and returns lines.
// Docker logs have an 8-byte header: [stream type (1), padding (3), size (4)]
func parseDockerLogs(reader io.Reader) ([]string, error) {
	var lines []string
	header := make([]byte, 8)

	for {
		// Read header
		_, err := io.ReadFull(reader, header)
		if err != nil {
			if err == io.EOF {
				break
			}
			return lines, nil // Return what we have
		}

		// Get payload size from header (big endian)
		size := binary.BigEndian.Uint32(header[4:8])
		if size == 0 {
			continue
		}

		// Read payload
		payload := make([]byte, size)
		_, err = io.ReadFull(reader, payload)
		if err != nil {
			if err == io.EOF {
				break
			}
			return lines, nil
		}

		// Split by newlines and add to result
		content := strings.TrimRight(string(payload), "\n\r")
		for _, line := range strings.Split(content, "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
	}

	return lines, nil
}

// streamDockerLogs streams Docker multiplexed log stream to a channel.
func streamDockerLogs(ctx context.Context, reader io.Reader, ch chan<- string) error {
	header := make([]byte, 8)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Read header
		_, err := io.ReadFull(reader, header)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		// Get payload size
		size := binary.BigEndian.Uint32(header[4:8])
		if size == 0 {
			continue
		}

		// Read payload
		payload := make([]byte, size)
		_, err = io.ReadFull(reader, payload)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}

		// Split by newlines and send to channel
		content := strings.TrimRight(string(payload), "\n\r")
		for _, line := range strings.Split(content, "\n") {
			if line != "" {
				select {
				case ch <- line:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}
