package deployment

import (
	"bufio"
	"context"
	"io"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// logTail returns the bounded recent log tail of the failing container.
// The read is bounded by the caller's deadline and by an internal cap so
// a slow or hung log stream cannot stall the operation.
func (s *Service) logTail(ctx context.Context, containerID string) []string {
	if containerID == "" || ctx.Err() != nil {
		return nil
	}
	tailCtx, cancel := context.WithTimeout(ctx, failLogTailTimeout)
	defer cancel()
	stream, err := s.deps.Runtime.GetContainerLogs(tailCtx, containerID, false)
	if err != nil {
		return nil
	}
	defer func() { _ = stream.Close() }()
	lines, err := tailLines(stream, failLogTailLines)
	if err != nil {
		return nil
	}
	return lines
}

// redactDiagnostics replaces every value of this service's secrets in the
// given log lines. Secret values are read into memory only. When a secret
// cannot be read, diagnostics are dropped entirely: unredacted application
// output is never persisted.
func (s *Service) redactDiagnostics(ctx context.Context, app string, p pinnedService, lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	if len(p.spec.Secrets) == 0 || s.deps.Secrets == nil {
		return lines
	}
	id, err := s.appSecretID(ctx, app)
	if err != nil {
		return nil
	}
	redacted := append([]string(nil), lines...)
	for _, name := range p.spec.Secrets {
		path := domain.AppSecretPathForID(id, app, p.spec.Name, name)
		value, err := s.deps.Secrets.GetSecret(ctx, path)
		if err != nil || value == "" {
			return nil
		}
		for i, line := range redacted {
			redacted[i] = strings.ReplaceAll(line, value, "[redacted]")
		}
	}
	return redacted
}

// tailLines keeps the last N lines; logs are untrusted app output.
func tailLines(r io.Reader, n int) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}
