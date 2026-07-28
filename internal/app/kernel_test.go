package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

func TestKernelMigrationCloserMutex(t *testing.T) {
	t.Parallel()

	kernel := &Kernel{}
	kernel.migrationInit = func() (*MigrationService, error) {
		time.Sleep(20 * time.Millisecond)
		closer := &testMigrationCloser{closed: make(chan struct{})}
		kernel.migrationMu.Lock()
		kernel.migrationCloser = closer
		kernel.migrationMu.Unlock()
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
