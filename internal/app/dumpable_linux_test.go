//go:build linux

package app

import (
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

	// readFileBeneath reads secrets through /proc/self/fd/N; it must keep
	// working once the process is non-dumpable.
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "secret"), []byte("value"), 0o600))
	data, err := readFileBeneath(root, "secret")
	require.NoError(t, err)
	assert.Equal(t, "value", string(data))
}
