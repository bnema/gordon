package logging

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"gordon/internal/config"
	"gordon/internal/testutils/mocks"
)

// mockLogStream implements io.ReadCloser for testing
type mockLogStream struct {
	data   string
	closed bool
	reader *strings.Reader
	ctx    context.Context
}

func newMockLogStream(data string) *mockLogStream {
	return &mockLogStream{
		data:   data,
		reader: strings.NewReader(data),
	}
}

func newContextAwareMockLogStream(ctx context.Context) *mockLogStream {
	return &mockLogStream{
		data:   "",
		reader: strings.NewReader(""),
		ctx:    ctx,
	}
}

func (m *mockLogStream) Read(p []byte) (n int, err error) {
	if m.closed {
		return 0, io.EOF
	}
	
	// If we have a context, check if it's cancelled
	if m.ctx != nil {
		select {
		case <-m.ctx.Done():
			return 0, io.EOF
		default:
			// Block until context is cancelled
			<-m.ctx.Done()
			return 0, io.EOF
		}
	}
	
	return m.reader.Read(p)
}

func (m *mockLogStream) Close() error {
	m.closed = true
	return nil
}

func TestNewLogManager(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cfg := &config.Config{}
	runtime := mocks.NewMockRuntime(ctrl)

	manager := NewLogManager(cfg, runtime)

	assert.NotNil(t, manager)
	assert.NotNil(t, manager.collectors)
	assert.Equal(t, cfg, manager.config)
	assert.Equal(t, runtime, manager.runtime)
	assert.Empty(t, manager.collectors)
}

func TestLogManager_StartCollection_LoggingDisabled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled: false,
		},
	}
	runtime := mocks.NewMockRuntime(ctrl)
	manager := NewLogManager(cfg, runtime)

	err := manager.StartCollection("container123", "test-container")
	assert.NoError(t, err)

	// Should not create any collectors
	assert.Equal(t, 0, manager.GetActiveCollectors())
}

func TestLogManager_StartCollection_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MainLogFile:       "gordon.log",
			ProxyLogFile:      "proxy.log",
			Level:             "info",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	runtime := mocks.NewMockRuntime(ctrl)
	manager := NewLogManager(cfg, runtime)

	containerID := "container123"
	containerName := "test-container"

	err := manager.StartCollection(containerID, containerName)
	require.NoError(t, err)

	// Should create one collector
	assert.Equal(t, 1, manager.GetActiveCollectors())

	// Check collector exists
	manager.mu.RLock()
	collector, exists := manager.collectors[containerID]
	manager.mu.RUnlock()

	assert.True(t, exists)
	assert.Equal(t, containerID, collector.containerID)
	assert.Equal(t, containerName, collector.containerName)
	assert.NotNil(t, collector.logWriter)
	assert.NotNil(t, collector.ctx)
	assert.NotNil(t, collector.cancel)

	// Clean up
	manager.StopAll()
}

func TestLogManager_StartCollection_ReplaceExisting(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MainLogFile:       "gordon.log",
			ProxyLogFile:      "proxy.log",
			Level:             "info",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	runtime := mocks.NewMockRuntime(ctrl)
	manager := NewLogManager(cfg, runtime)

	containerID := "container123"

	// Start first collector
	err := manager.StartCollection(containerID, "first-container")
	require.NoError(t, err)
	assert.Equal(t, 1, manager.GetActiveCollectors())

	// Get reference to first collector
	manager.mu.RLock()
	firstCollector := manager.collectors[containerID]
	manager.mu.RUnlock()

	// Start second collector with same ID (should replace)
	err = manager.StartCollection(containerID, "second-container")
	require.NoError(t, err)
	assert.Equal(t, 1, manager.GetActiveCollectors())

	// Get reference to second collector
	manager.mu.RLock()
	secondCollector := manager.collectors[containerID]
	manager.mu.RUnlock()

	// Should be different collectors
	assert.NotEqual(t, firstCollector, secondCollector)
	assert.Equal(t, "second-container", secondCollector.containerName)

	// Clean up
	manager.StopAll()
}

func TestLogManager_StopCollection(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MainLogFile:       "gordon.log",
			ProxyLogFile:      "proxy.log",
			Level:             "info",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	runtime := mocks.NewMockRuntime(ctrl)
	manager := NewLogManager(cfg, runtime)

	containerID := "container123"

	// Start collector
	err := manager.StartCollection(containerID, "test-container")
	require.NoError(t, err)
	assert.Equal(t, 1, manager.GetActiveCollectors())

	// Stop collector
	manager.StopCollection(containerID)
	assert.Equal(t, 0, manager.GetActiveCollectors())

	// Stopping non-existent collector should not panic
	manager.StopCollection("non-existent")
	assert.Equal(t, 0, manager.GetActiveCollectors())
}

func TestLogManager_StopAll(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MainLogFile:       "gordon.log",
			ProxyLogFile:      "proxy.log",
			Level:             "info",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	runtime := mocks.NewMockRuntime(ctrl)
	
	// Mock GetContainerLogs calls for each container - return context-aware streams
	runtime.EXPECT().GetContainerLogs(gomock.Any(), "container1", true).DoAndReturn(
		func(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
			return newContextAwareMockLogStream(ctx), nil
		}).AnyTimes()
	runtime.EXPECT().GetContainerLogs(gomock.Any(), "container2", true).DoAndReturn(
		func(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
			return newContextAwareMockLogStream(ctx), nil
		}).AnyTimes()
	runtime.EXPECT().GetContainerLogs(gomock.Any(), "container3", true).DoAndReturn(
		func(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
			return newContextAwareMockLogStream(ctx), nil
		}).AnyTimes()
	
	manager := NewLogManager(cfg, runtime)

	// Start multiple collectors
	err := manager.StartCollection("container1", "test-container1")
	require.NoError(t, err)
	err = manager.StartCollection("container2", "test-container2")
	require.NoError(t, err)
	err = manager.StartCollection("container3", "test-container3")
	require.NoError(t, err)

	assert.Equal(t, 3, manager.GetActiveCollectors())

	// Stop all collectors
	manager.StopAll()
	assert.Equal(t, 0, manager.GetActiveCollectors())

	// Stopping again should be safe
	manager.StopAll()
	assert.Equal(t, 0, manager.GetActiveCollectors())
}

func TestLogManager_GetActiveCollectors(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled: false,
		},
	}
	runtime := mocks.NewMockRuntime(ctrl)
	manager := NewLogManager(cfg, runtime)

	// Initially no collectors
	assert.Equal(t, 0, manager.GetActiveCollectors())

	// Test concurrent access
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count := manager.GetActiveCollectors()
			assert.GreaterOrEqual(t, count, 0)
		}()
	}
	wg.Wait()
}

func TestLogCollector_Stop(t *testing.T) {
	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	logWriter, err := GetContainerLogWriter(cfg, "test123", "test-container")
	require.NoError(t, err)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	collector := &LogCollector{
		containerID:   "test123",
		containerName: "test-container",
		runtime:       mocks.NewMockRuntime(ctrl),
		logWriter:     logWriter,
		ctx:           ctx,
		cancel:        cancel,
	}

	// Test that Stop doesn't panic
	assert.NotPanics(t, func() {
		collector.Stop()
	})

	// Test that Stop can be called multiple times
	assert.NotPanics(t, func() {
		collector.Stop()
	})
}

func TestLogCollector_CollectLogsOnce_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	logWriter, err := GetContainerLogWriter(cfg, "test123", "test-container")
	require.NoError(t, err)

	// Create mock runtime that returns log stream
	logData := "line 1\nline 2\nline 3"
	mockStream := newMockLogStream(logData)
	
	runtime := mocks.NewMockRuntime(ctrl)
	runtime.EXPECT().GetContainerLogs(gomock.Any(), "test123", true).Return(mockStream, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	collector := &LogCollector{
		containerID:   "test123",
		containerName: "test-container",
		runtime:       runtime,
		logWriter:     logWriter,
		ctx:           ctx,
		cancel:        cancel,
	}

	err = collector.collectLogsOnce()
	assert.NoError(t, err)
	assert.True(t, mockStream.closed)
}

func TestLogCollector_CollectLogsOnce_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	logWriter, err := GetContainerLogWriter(cfg, "test123", "test-container")
	require.NoError(t, err)

	// Create mock runtime that returns error
	runtime := mocks.NewMockRuntime(ctrl)
	runtime.EXPECT().GetContainerLogs(gomock.Any(), "test123", true).Return(nil, errors.New("failed to get logs"))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	collector := &LogCollector{
		containerID:   "test123",
		containerName: "test-container",
		runtime:       runtime,
		logWriter:     logWriter,
		ctx:           ctx,
		cancel:        cancel,
	}

	err = collector.collectLogsOnce()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get log stream")
}

func TestLogCollector_CollectLogsOnce_ContextCancelled(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	logWriter, err := GetContainerLogWriter(cfg, "test123", "test-container")
	require.NoError(t, err)

	// Create mock stream that never ends
	mockStream := newMockLogStream("line 1\nline 2\n")
	
	runtime := mocks.NewMockRuntime(ctrl)
	runtime.EXPECT().GetContainerLogs(gomock.Any(), "test123", true).Return(mockStream, nil)

	ctx, cancel := context.WithCancel(context.Background())
	
	collector := &LogCollector{
		containerID:   "test123",
		containerName: "test-container",
		runtime:       runtime,
		logWriter:     logWriter,
		ctx:           ctx,
		cancel:        cancel,
	}

	// Cancel context immediately
	cancel()

	err = collector.collectLogsOnce()
	assert.NoError(t, err) // Should return nil when context is cancelled
}

func TestLogCollector_CollectLogs_RetryLogic(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	logWriter, err := GetContainerLogWriter(cfg, "test123", "test-container")
	require.NoError(t, err)

	// Create mock runtime that fails first time, succeeds second time
	runtime := mocks.NewMockRuntime(ctrl)
	runtime.EXPECT().GetContainerLogs(gomock.Any(), "test123", true).
		Return(nil, errors.New("temporary failure")).Times(1)
	runtime.EXPECT().GetContainerLogs(gomock.Any(), "test123", true).DoAndReturn(
		func(ctx context.Context, containerID string, follow bool) (io.ReadCloser, error) {
			return newContextAwareMockLogStream(ctx), nil
		}).AnyTimes()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	collector := &LogCollector{
		containerID:   "test123",
		containerName: "test-container",
		runtime:       runtime,
		logWriter:     logWriter,
		ctx:           ctx,
		cancel:        cancel,
	}

	// Start collection in goroutine
	collector.wg.Add(1)
	go collector.collectLogs()

	// Wait a bit for retry to happen
	time.Sleep(1500 * time.Millisecond)
	
	// Cancel and wait for completion
	cancel()
	collector.wg.Wait()

	// Test passes if no panic occurs and the mocks were called as expected
}

func TestLogCollector_CollectLogs_ContextCancellation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tempDir := t.TempDir()
	cfg := &config.Config{
		Logging: config.LoggingConfig{
			Enabled:           true,
			Dir:               tempDir,
			ContainerLogDir:   "containers",
			MaxSize:           10,
			MaxBackups:        3,
			MaxAge:            7,
			Compress:          false,
		},
	}

	logWriter, err := GetContainerLogWriter(cfg, "test123", "test-container")
	require.NoError(t, err)

	runtime := mocks.NewMockRuntime(ctrl)
	// Should not be called if context is cancelled immediately

	ctx, cancel := context.WithCancel(context.Background())

	collector := &LogCollector{
		containerID:   "test123",
		containerName: "test-container",
		runtime:       runtime,
		logWriter:     logWriter,
		ctx:           ctx,
		cancel:        cancel,
	}

	// Cancel context immediately
	cancel()

	// Start collection
	collector.wg.Add(1)
	go collector.collectLogs()

	// Wait for completion
	collector.wg.Wait()

	// Test passes if no panic occurs and goroutine exits cleanly
}