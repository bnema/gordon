package app

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeRoleListenerUnixSocketRestrictsPermissionsRemovesStaleAndCleansUp(t *testing.T) {
	data := shortRuntimeSocketTestDir(t)
	path := filepath.Join(data, "migration", "fixture", bootstrapRuntimeSocketName)
	endpoint := "unix://" + path
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	stale, err := net.Listen("unix", path)
	require.NoError(t, err)
	require.NoError(t, stale.Close())

	listener, cleanup, err := runtimeRoleListener(net.Listen, endpoint, data)
	require.NoError(t, err)
	require.Equal(t, "unix", listener.Addr().Network())
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSocket)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	parent, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), parent.Mode().Perm())
	require.NoError(t, listener.Close())
	cleanup()
	_, err = os.Lstat(path)
	assert.True(t, os.IsNotExist(err))
}

func TestRuntimeRoleListenerRejectsTraversalSymlinkAndNonSocket(t *testing.T) {
	data := shortRuntimeSocketTestDir(t)
	for _, endpoint := range []string{
		"unix://" + data + "/migration/fixture/../" + bootstrapRuntimeSocketName,
		"unix:///tmp/runtime-control.sock",
	} {
		_, _, err := runtimeRoleListener(net.Listen, endpoint, data)
		require.Error(t, err, endpoint)
	}
	path := filepath.Join(data, "migration", "fixture", bootstrapRuntimeSocketName)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("not-a-socket"), 0o600))
	_, _, err := runtimeRoleListener(net.Listen, "unix://"+path, data)
	require.Error(t, err)

	linkData := shortRuntimeSocketTestDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(linkData, "real"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(linkData, "migration"), 0o700))
	require.NoError(t, os.Symlink(filepath.Join(linkData, "real"), filepath.Join(linkData, "migration", "fixture")))
	_, _, err = runtimeRoleListener(net.Listen, "unix://"+filepath.Join(linkData, "migration", "fixture", bootstrapRuntimeSocketName), linkData)
	require.Error(t, err)
}

func shortRuntimeSocketTestDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "gr-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}
