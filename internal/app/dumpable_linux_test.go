//go:build linux

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestDisableDumpable(t *testing.T) {
	t.Cleanup(func() { _ = unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0) })

	require.NoError(t, disableDumpable())
	got, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, got)

	// readFileBeneath reads secrets through /proc/self/fd/N; the kernel
	// keeps that self access for a non-dumpable process.
	path := filepath.Join(t.TempDir(), "secret")
	require.NoError(t, os.WriteFile(path, []byte("value"), 0o600))
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	defer unix.Close(fd)
	data, err := os.ReadFile(fmt.Sprintf("/proc/self/fd/%d", fd))
	require.NoError(t, err)
	assert.Equal(t, "value", string(data))
}
