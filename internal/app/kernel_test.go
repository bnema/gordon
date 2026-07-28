package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewKernel(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "gordon.toml")
	dataDir := filepath.Join(tmpDir, "data")

	cfg := fmt.Sprintf(`[server]
gordon_domain = "gordon.local"
data_dir = %q

[auth]
enabled = false
secrets_backend = "unsafe"
`, dataDir)

	err := os.WriteFile(cfgPath, []byte(cfg), 0o600)
	require.NoError(t, err)

	kernel, err := NewKernel(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, kernel)
	t.Cleanup(func() { require.NoError(t, kernel.Close()) })

	require.NotNil(t, kernel.Config())
	require.NotNil(t, kernel.Secrets())
}

type testMigrationCloser struct {
	closed chan struct{}
}

func (c *testMigrationCloser) Close() error {
	close(c.closed)
	return nil
}

type countingMigrationCloser struct {
	mu     sync.Mutex
	closes int
}

func (c *countingMigrationCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	return nil
}

func (c *countingMigrationCloser) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

func TestKernelMigrationCloserMutex(t *testing.T) {
	t.Parallel()

	kernel := &Kernel{}
	kernel.migrationInit = func() (*MigrationService, error) {
		time.Sleep(20 * time.Millisecond)
		closer := &testMigrationCloser{closed: make(chan struct{})}
		kernel.installMigrationCloser(closer)
		return nil, nil
	}

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = kernel.Migration()
		}()
		go func() {
			defer wg.Done()
			_ = kernel.Close()
		}()
	}
	wg.Wait()
}

func TestKernelMigrationCloserCannotInstallAfterClose(t *testing.T) {
	t.Parallel()

	closer := &countingMigrationCloser{}
	releaseInit := make(chan struct{})
	initStarted := make(chan struct{})

	kernel := &Kernel{}
	kernel.migrationInit = func() (*MigrationService, error) {
		close(initStarted)
		<-releaseInit
		kernel.installMigrationCloser(closer)
		return nil, nil
	}

	go func() { _, _ = kernel.Migration() }()
	<-initStarted
	require.NoError(t, kernel.Close())
	close(releaseInit)

	require.Eventually(t, func() bool {
		kernel.migrationMu.Lock()
		defer kernel.migrationMu.Unlock()
		return kernel.migrationCloser == nil && kernel.migrationClosed
	}, time.Second, 5*time.Millisecond)

	assert.Equal(t, 1, closer.count(), "orphan closer created after Close must be closed exactly once")
	require.NoError(t, kernel.Close())
	assert.Equal(t, 1, closer.count(), "repeated Close must stay idempotent")
}

func TestKernelMigrationCloserClosesExactlyOnceUnderRace(t *testing.T) {
	t.Parallel()

	closer := &countingMigrationCloser{}
	start := make(chan struct{})
	var wg sync.WaitGroup

	kernel := &Kernel{}
	kernel.migrationInit = func() (*MigrationService, error) {
		<-start
		kernel.installMigrationCloser(closer)
		return nil, nil
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = kernel.Migration()
	}()
	go func() {
		defer wg.Done()
		<-start
		_ = kernel.Close()
	}()
	close(start)
	wg.Wait()

	for range 5 {
		require.NoError(t, kernel.Close())
	}
	assert.Equal(t, 1, closer.count())
	kernel.migrationMu.Lock()
	assert.Nil(t, kernel.migrationCloser)
	assert.True(t, kernel.migrationClosed)
	kernel.migrationMu.Unlock()
}
