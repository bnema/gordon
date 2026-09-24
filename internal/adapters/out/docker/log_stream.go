package docker

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// maxLogLineBytes bounds one exported line; longer lines are truncated
// so one runaway writer cannot exhaust memory.
const maxLogLineBytes = 64 * 1024

var _ out.ContainerLogStreamer = (*Runtime)(nil)

// StreamContainerLogs implements out.ContainerLogStreamer. It follows
// stdout and stderr with runtime timestamps, demultiplexes them, and
// emits one line at a time until the stream ends or ctx is canceled.
func (r *Runtime) StreamContainerLogs(ctx context.Context, containerID string, since time.Time, emit func(domain.ContainerLogLine)) error {
	options := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Timestamps: true,
	}
	if !since.IsZero() {
		options.Since = since.UTC().Format(time.RFC3339Nano)
	}
	logs, err := r.client.ContainerLogs(ctx, containerID, options)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("%w: %s", domain.ErrContainerNotFound, containerID)
		}
		return fmt.Errorf("follow container logs: %w", err)
	}
	defer logs.Close()
	// Closing the stream unblocks the demux copy on cancellation.
	stop := context.AfterFunc(ctx, func() { _ = logs.Close() })
	defer stop()

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	done := make(chan struct{}, 2)
	lines := make(chan domain.ContainerLogLine)
	go scanLogLines(stdoutR, domain.LogStreamStdout, lines, done)
	go scanLogLines(stderrR, domain.LogStreamStderr, lines, done)

	copyErr := make(chan error, 1)
	go func() {
		_, err := stdcopy.StdCopy(stdoutW, stderrW, logs)
		_ = stdoutW.Close()
		_ = stderrW.Close()
		copyErr <- err
	}()

	for pending := 2; pending > 0; {
		select {
		case line := <-lines:
			emit(line)
		case <-done:
			pending--
		}
	}
	err = <-copyErr
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("demultiplex container logs: %w", err)
	}
	return nil
}

// scanLogLines splits one demultiplexed stream into timestamped lines.
// The reader is always drained so the demux copy never blocks.
func scanLogLines(r *io.PipeReader, stream string, lines chan<- domain.ContainerLogLine, done chan<- struct{}) {
	defer func() { done <- struct{}{} }()
	reader := bufio.NewReaderSize(r, 4096)
	for {
		raw, err := readBoundedLine(reader)
		if raw != "" {
			lines <- parseTimestampedLine(raw, stream)
		}
		if err != nil {
			_ = r.CloseWithError(err)
			return
		}
	}
}

// readBoundedLine returns one line without its newline, truncated to
// maxLogLineBytes; the rest of an oversized line is discarded.
func readBoundedLine(reader *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		chunk, err := reader.ReadSlice('\n')
		if room := maxLogLineBytes - b.Len(); room > 0 {
			if len(chunk) > room {
				chunk = chunk[:room]
			}
			b.Write(chunk)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return strings.TrimRight(b.String(), "\r\n"), err
	}
}

// parseTimestampedLine splits the RFC3339Nano prefix Docker adds when
// Timestamps is set. Lines without a valid prefix keep the receive time.
func parseTimestampedLine(raw, stream string) domain.ContainerLogLine {
	line := domain.ContainerLogLine{Time: time.Now().UTC(), Stream: stream, Body: raw}
	prefix, body, ok := strings.Cut(raw, " ")
	if !ok {
		return line
	}
	if ts, err := time.Parse(time.RFC3339Nano, prefix); err == nil {
		line.Time = ts
		line.Body = body
	}
	return line
}
