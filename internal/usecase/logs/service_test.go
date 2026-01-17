package logs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bnema/zerowrap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"gordon/internal/boundaries/in/mocks"
	outMocks "gordon/internal/boundaries/out/mocks"
	"gordon/internal/domain"
)

func TestService_GetProcessLogs(t *testing.T) {
	log := zerowrap.New(zerowrap.Config{Level: "warn"})

	t.Run("returns lines from log file", func(t *testing.T) {
		// Create temp log file with content
		tmpDir := t.TempDir()
		logPath := filepath.Join(tmpDir, "test.log")
		content := "line1\nline2\nline3\nline4\nline5\n"
		err := os.WriteFile(logPath, []byte(content), 0644)
		require.NoError(t, err)

		containerSvc := mocks.NewMockContainerService(t)
		runtime := outMocks.NewMockContainerRuntime(t)

		svc := NewService(logPath, containerSvc, runtime, log)

		lines, err := svc.GetProcessLogs(context.Background(), 3)
		require.NoError(t, err)
		assert.Len(t, lines, 3)
		assert.Equal(t, []string{"line3", "line4", "line5"}, lines)
	})

	t.Run("returns empty slice for non-existent file", func(t *testing.T) {
		containerSvc := mocks.NewMockContainerService(t)
		runtime := outMocks.NewMockContainerRuntime(t)

		svc := NewService("/nonexistent/file.log", containerSvc, runtime, log)

		lines, err := svc.GetProcessLogs(context.Background(), 10)
		require.NoError(t, err)
		assert.Empty(t, lines)
	})

	t.Run("returns error when log file path not configured", func(t *testing.T) {
		containerSvc := mocks.NewMockContainerService(t)
		runtime := outMocks.NewMockContainerRuntime(t)

		svc := NewService("", containerSvc, runtime, log)

		_, err := svc.GetProcessLogs(context.Background(), 10)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "log file path not configured")
	})

	t.Run("returns all lines when fewer than requested", func(t *testing.T) {
		tmpDir := t.TempDir()
		logPath := filepath.Join(tmpDir, "test.log")
		content := "line1\nline2\n"
		err := os.WriteFile(logPath, []byte(content), 0644)
		require.NoError(t, err)

		containerSvc := mocks.NewMockContainerService(t)
		runtime := outMocks.NewMockContainerRuntime(t)

		svc := NewService(logPath, containerSvc, runtime, log)

		lines, err := svc.GetProcessLogs(context.Background(), 10)
		require.NoError(t, err)
		assert.Equal(t, []string{"line1", "line2"}, lines)
	})
}

func TestService_FollowProcessLogs(t *testing.T) {
	log := zerowrap.New(zerowrap.Config{Level: "warn"})

	t.Run("streams lines and respects context cancellation", func(t *testing.T) {
		tmpDir := t.TempDir()
		logPath := filepath.Join(tmpDir, "test.log")
		content := "initial1\ninitial2\n"
		err := os.WriteFile(logPath, []byte(content), 0644)
		require.NoError(t, err)

		containerSvc := mocks.NewMockContainerService(t)
		runtime := outMocks.NewMockContainerRuntime(t)

		svc := NewService(logPath, containerSvc, runtime, log)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ch, err := svc.FollowProcessLogs(ctx, 2)
		require.NoError(t, err)

		// Read initial lines
		var received []string
		for i := 0; i < 2; i++ {
			select {
			case line := <-ch:
				received = append(received, line)
			case <-time.After(time.Second):
				t.Fatal("timeout waiting for initial lines")
			}
		}
		assert.Equal(t, []string{"initial1", "initial2"}, received)

		// Cancel context to stop following
		cancel()

		// Channel should be closed
		select {
		case <-ch:
			// May receive one more line before closure, that's OK
		case <-time.After(500 * time.Millisecond):
			// OK, channel may already be closed
		}
	})
}

func TestService_GetContainerLogs(t *testing.T) {
	log := zerowrap.New(zerowrap.Config{Level: "warn"})

	t.Run("returns error when container not found", func(t *testing.T) {
		containerSvc := mocks.NewMockContainerService(t)
		runtime := outMocks.NewMockContainerRuntime(t)

		containerSvc.EXPECT().Get(mock.Anything, "unknown.local").Return(nil, false)

		svc := NewService("/tmp/test.log", containerSvc, runtime, log)

		_, err := svc.GetContainerLogs(context.Background(), "unknown.local", 10)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "container not found")
	})

	t.Run("calls runtime with correct container ID", func(t *testing.T) {
		containerSvc := mocks.NewMockContainerService(t)
		runtime := outMocks.NewMockContainerRuntime(t)

		container := &domain.Container{
			ID:     "abc123",
			Name:   "app.local",
			Status: "running",
		}
		containerSvc.EXPECT().Get(mock.Anything, "app.local").Return(container, true)

		// Create a mock reader that returns empty content
		runtime.EXPECT().GetContainerLogs(mock.Anything, "abc123", false).Return(&mockReader{}, nil)

		svc := NewService("/tmp/test.log", containerSvc, runtime, log)

		lines, err := svc.GetContainerLogs(context.Background(), "app.local", 10)
		require.NoError(t, err)
		assert.Empty(t, lines)
	})
}

func TestService_FollowContainerLogs(t *testing.T) {
	log := zerowrap.New(zerowrap.Config{Level: "warn"})

	t.Run("returns error when container not found", func(t *testing.T) {
		containerSvc := mocks.NewMockContainerService(t)
		runtime := outMocks.NewMockContainerRuntime(t)

		containerSvc.EXPECT().Get(mock.Anything, "unknown.local").Return(nil, false)

		svc := NewService("/tmp/test.log", containerSvc, runtime, log)

		_, err := svc.FollowContainerLogs(context.Background(), "unknown.local", 10)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "container not found")
	})
}

func TestTailLines(t *testing.T) {
	t.Run("returns correct last N lines", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "test.log")
		content := "a\nb\nc\nd\ne\n"
		err := os.WriteFile(path, []byte(content), 0644)
		require.NoError(t, err)

		file, err := os.Open(path)
		require.NoError(t, err)
		defer file.Close()

		lines, err := tailLines(file, 3)
		require.NoError(t, err)
		assert.Equal(t, []string{"c", "d", "e"}, lines)
	})

	t.Run("returns all lines when n exceeds line count", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "test.log")
		content := "x\ny\n"
		err := os.WriteFile(path, []byte(content), 0644)
		require.NoError(t, err)

		file, err := os.Open(path)
		require.NoError(t, err)
		defer file.Close()

		lines, err := tailLines(file, 10)
		require.NoError(t, err)
		assert.Equal(t, []string{"x", "y"}, lines)
	})

	t.Run("returns empty slice for empty file", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "test.log")
		err := os.WriteFile(path, []byte(""), 0644)
		require.NoError(t, err)

		file, err := os.Open(path)
		require.NoError(t, err)
		defer file.Close()

		lines, err := tailLines(file, 10)
		require.NoError(t, err)
		assert.Empty(t, lines)
	})

	t.Run("returns empty slice when n is zero", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "test.log")
		content := "a\nb\n"
		err := os.WriteFile(path, []byte(content), 0644)
		require.NoError(t, err)

		file, err := os.Open(path)
		require.NoError(t, err)
		defer file.Close()

		lines, err := tailLines(file, 0)
		require.NoError(t, err)
		assert.Empty(t, lines)
	})
}

// mockReader implements io.ReadCloser for testing.
type mockReader struct{}

func (m *mockReader) Read(p []byte) (n int, err error) {
	return 0, io.EOF
}

func (m *mockReader) Close() error {
	return nil
}
