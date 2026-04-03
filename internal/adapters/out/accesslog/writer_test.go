package accesslog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	out "github.com/bnema/gordon/internal/boundaries/out"
)

var sampleEntry = out.AccessLogEntry{
	Time:       time.Date(2026, 4, 3, 14, 20, 54, 167_000_000, time.UTC),
	ClientIP:   "10.0.0.1",
	Method:     "POST",
	Host:       "api.example.com",
	Path:       "/api/v1/data",
	Query:      "limit=10",
	Status:     200,
	BytesSent:  512,
	DurationMS: 3.5,
	UserAgent:  "curl/7.88",
	Referer:    "https://example.com",
	RequestID:  "abc123",
	Proto:      "HTTP/2.0",
}

// --- New() validation ---

func TestNew_InvalidFormat(t *testing.T) {
	_, err := New(Config{Format: "xml", Output: "stdout"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid logging.access_log.format")
}

func TestNew_InvalidOutput(t *testing.T) {
	_, err := New(Config{Format: "json", Output: "kafka"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid logging.access_log.output")
}

func TestNew_FileOutput_EmptyPath(t *testing.T) {
	_, err := New(Config{Format: "json", Output: "file", FilePath: ""})
	require.Error(t, err)
}

func TestNew_FileOutput_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "access.log")
	w, err := New(Config{
		Format:   "json",
		Output:   "file",
		FilePath: path,
		MaxSize:  10,
	})
	require.NoError(t, err)
	defer w.Close()

	assert.DirExists(t, filepath.Join(dir, "sub"))
}

// --- stdout sink ---

func TestWriter_Stdout_JSON(t *testing.T) {
	// Replace stdout to capture output.
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer, err := New(Config{Format: "json", Output: "stdout"})
	require.NoError(t, err)

	require.NoError(t, writer.Write(sampleEntry))
	require.NoError(t, writer.Close())

	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	line := strings.TrimSpace(buf.String())

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(line), &got), "output must be valid JSON: %s", line)
	assert.Equal(t, "10.0.0.1", got["client_ip"])
	assert.Equal(t, "POST", got["method"])
	assert.InDelta(t, 200, got["status"], 0)
}

func TestWriter_Stdout_CLF(t *testing.T) {
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer, err := New(Config{Format: "clf", Output: "stdout"})
	require.NoError(t, err)
	require.NoError(t, writer.Write(sampleEntry))
	require.NoError(t, writer.Close())

	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	line := strings.TrimSpace(buf.String())

	assert.True(t, strings.HasPrefix(line, "10.0.0.1 - - ["))
	assert.Contains(t, line, `"POST /api/v1/data?limit=10 HTTP/2.0"`)
	assert.Contains(t, line, " 200 512")
}

func TestWriter_Stdout_Combined(t *testing.T) {
	orig := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	writer, err := New(Config{Format: "combined", Output: "stdout"})
	require.NoError(t, err)
	require.NoError(t, writer.Write(sampleEntry))
	require.NoError(t, writer.Close())

	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	line := strings.TrimSpace(buf.String())

	assert.Contains(t, line, `"https://example.com"`)
	assert.Contains(t, line, `"curl/7.88"`)
}

// --- file sink ---

func TestWriter_File_WritesLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")

	writer, err := New(Config{
		Format:     "json",
		Output:     "file",
		FilePath:   path,
		MaxSize:    10,
		MaxBackups: 1,
	})
	require.NoError(t, err)

	require.NoError(t, writer.Write(sampleEntry))
	require.NoError(t, writer.Write(sampleEntry))
	require.NoError(t, writer.Close())

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, 2)
	for _, l := range lines {
		var obj map[string]any
		assert.NoError(t, json.Unmarshal([]byte(l), &obj), "each line must be valid JSON")
	}
}

// --- concurrency ---

func TestWriter_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "concurrent.log")

	writer, err := New(Config{
		Format:   "json",
		Output:   "file",
		FilePath: path,
		MaxSize:  100,
	})
	require.NoError(t, err)
	defer writer.Close()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(n int) {
			defer wg.Done()
			e := sampleEntry
			e.RequestID = fmt.Sprintf("req-%d", n)
			assert.NoError(t, writer.Write(e))
		}(i)
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Len(t, lines, goroutines, "each goroutine should produce exactly one line")
	for _, l := range lines {
		var obj map[string]any
		assert.NoError(t, json.Unmarshal([]byte(l), &obj), "each line must be valid JSON")
	}
}
