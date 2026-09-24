package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestRuntime_StreamContainerLogs_DemuxesTimestampedLines(t *testing.T) {
	var gotFollow string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFollow = r.URL.Query().Get("follow")
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		_, _ = w.Write(muxFrame(stdcopy.Stdout, "2026-09-10T12:00:01.5Z hello\n"))
		_, _ = w.Write(muxFrame(stdcopy.Stderr, "2026-09-10T12:00:02Z boom\n"))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	var lines []domain.ContainerLogLine
	err := runtime.StreamContainerLogs(context.Background(), "c1", time.Time{}, func(l domain.ContainerLogLine) {
		lines = append(lines, l)
	})

	require.NoError(t, err)
	assert.Equal(t, "1", gotFollow)
	require.Len(t, lines, 2)
	byStream := map[string]domain.ContainerLogLine{}
	for _, l := range lines {
		byStream[l.Stream] = l
	}
	assert.Equal(t, "hello", byStream[domain.LogStreamStdout].Body)
	assert.Equal(t, time.Date(2026, 9, 10, 12, 0, 1, 500000000, time.UTC), byStream[domain.LogStreamStdout].Time)
	assert.Equal(t, "boom", byStream[domain.LogStreamStderr].Body)
}

// muxFrame encodes one Docker multiplexed-stream frame.
func muxFrame(stream stdcopy.StdType, payload string) []byte {
	frame := make([]byte, 8, 8+len(payload))
	frame[0] = byte(stream)
	binary.BigEndian.PutUint32(frame[4:], uint32(len(payload)))
	return append(frame, payload...)
}

func TestRuntime_StreamContainerLogs_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"No such container: gone"}`))
	}))
	defer server.Close()

	runtime := newRuntimeForHTTPServer(t, server)
	err := runtime.StreamContainerLogs(context.Background(), "gone", time.Time{}, func(domain.ContainerLogLine) {})

	assert.ErrorIs(t, err, domain.ErrContainerNotFound)
}

func TestReadBoundedLine_TruncatesOversizedLines(t *testing.T) {
	long := strings.Repeat("x", maxLogLineBytes+100)
	reader := bufio.NewReaderSize(strings.NewReader(long+"\nnext\n"), 16)

	first, err := readBoundedLine(reader)
	require.NoError(t, err)
	second, err := readBoundedLine(reader)
	require.NoError(t, err)

	assert.Len(t, first, maxLogLineBytes)
	assert.Equal(t, "next", second)
}

func TestParseTimestampedLine_KeepsBodyWithoutTimestamp(t *testing.T) {
	line := parseTimestampedLine("plain text line", domain.LogStreamStdout)
	assert.Equal(t, "plain text line", line.Body)
}
