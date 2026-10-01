package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bnema/zerowrap"
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
}

// TestKernelClose_QuiescenceTimeoutSkipsCleanup proves the fail-closed kernel
// Close ordering: daemon-owned app administration is cancelled and joined on
// a bounded context before any remaining kernel resource is torn down, and
// when that quiescence times out the dependency teardown is skipped and the
// error is returned, so an unfinished deploy is never dropped onto a closed
// state or runtime.
func TestKernelClose_QuiescenceTimeoutSkipsCleanup(t *testing.T) {
	t.Parallel()

	order := []string{}
	admin := &orderRecordingAdmin{order: &order, err: errors.New("in-flight deploy did not unwind")}
	cleanedUp := false
	kernel := &Kernel{
		appAdmin: admin,
		log:      zerowrap.Default(),
		cleanup:  func() { cleanedUp = true; order = append(order, "cleanup") },
	}

	require.Error(t, kernel.Close())
	require.Equal(t, []string{"app-shutdown"}, order, "teardown must stop when quiescence times out")
	assert.False(t, cleanedUp)
	assert.True(t, admin.deadline, "app administration shutdown must run on a bounded context")
}

// TestKernelClose_CleansUpAfterQuiescence keeps the normal path: once app
// administration joined cleanly, the kernel still tears its resources down.
func TestKernelClose_CleansUpAfterQuiescence(t *testing.T) {
	t.Parallel()

	order := []string{}
	admin := &orderRecordingAdmin{order: &order}
	kernel := &Kernel{
		appAdmin: admin,
		log:      zerowrap.Default(),
		cleanup:  func() { order = append(order, "cleanup") },
	}

	require.NoError(t, kernel.Close())
	require.Equal(t, []string{"app-shutdown", "cleanup"}, order)
}

// TestKernelClose_WithoutAppAdministrationStillCleansUp keeps the minimal
// kernel path (no full service wiring) closing without a nil-interface trap.
func TestKernelClose_WithoutAppAdministrationStillCleansUp(t *testing.T) {
	t.Parallel()

	cleaned := false
	kernel := &Kernel{log: zerowrap.Default(), cleanup: func() { cleaned = true }}

	require.NoError(t, kernel.Close())
	assert.True(t, cleaned)
}
