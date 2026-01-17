// Package logs implements the log access use case.
package logs

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"gordon/internal/boundaries/in"
	"gordon/internal/boundaries/out"
)

// Service implements the LogService interface.
type Service struct {
	logFilePath  string
	containerSvc in.ContainerService
	runtime      out.ContainerRuntime
	log          zerowrap.Logger
}

// NewService creates a new log service.
func NewService(
	logFilePath string,
	containerSvc in.ContainerService,
	runtime out.ContainerRuntime,
	log zerowrap.Logger,
) *Service {
	return &Service{
		logFilePath:  logFilePath,
		containerSvc: containerSvc,
		runtime:      runtime,
		log:          log,
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

	if s.logFilePath == "" {
		return nil, fmt.Errorf("log file path not configured")
	}

	file, err := os.Open(s.logFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, log.WrapErr(err, "failed to open log file")
	}
	defer file.Close()

	return tailLines(file, lines)
}

// FollowProcessLogs returns a channel that streams Gordon process log lines.
func (s *Service) FollowProcessLogs(ctx context.Context, initialLines int) (<-chan string, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "FollowProcessLogs",
		"initial_lines":       initialLines,
	})
	log := zerowrap.FromCtx(ctx)

	if s.logFilePath == "" {
		return nil, fmt.Errorf("log file path not configured")
	}

	file, err := os.Open(s.logFilePath)
	if err != nil {
		return nil, log.WrapErr(err, "failed to open log file")
	}

	ch := make(chan string, 100)

	go func() {
		defer close(ch)
		defer file.Close()

		// First, get initial lines
		if initialLines > 0 {
			lines, err := tailLines(file, initialLines)
			if err != nil {
				log.Warn().Err(err).Msg("failed to read initial lines")
			} else {
				for _, line := range lines {
					select {
					case ch <- line:
					case <-ctx.Done():
						return
					}
				}
			}
		}

		// Seek to end for following
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			log.Warn().Err(err).Msg("failed to seek to end")
			return
		}

		// Follow new lines
		reader := bufio.NewReader(file)
		for {
			select {
			case <-ctx.Done():
				return
			default:
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
				select {
				case ch <- line:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch, nil
}

// GetContainerLogs returns the last N lines of container logs for a domain.
func (s *Service) GetContainerLogs(ctx context.Context, domain string, lines int) ([]string, error) {
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
