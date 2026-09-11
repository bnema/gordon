package localadmin

import (
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureRuntimeDirPrefersXDG(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	dir, err := EnsureRuntimeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(xdg, "gordon"), dir)

	fi, err := os.Lstat(dir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), fi.Mode().Perm())
}

func TestEnsureRuntimeDirFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", home)

	dir, err := EnsureRuntimeDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".gordon", "run"), dir)
}

func TestEnsureRuntimeDirRejectsUnsafeXDG(t *testing.T) {
	xdg := t.TempDir()
	// Make the selected XDG gordon path a symlink. Falling back would silently
	// change daemon identity, so an unsafe selected path fails closed.
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(xdg, "gordon")))
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	t.Setenv("HOME", t.TempDir())

	_, err := EnsureRuntimeDir()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestEnsureRuntimeDirRejectsRelativeXDG(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join("relative", "runtime"))
	t.Setenv("HOME", home)

	_, err := EnsureRuntimeDir()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
	// Fail closed: no silent fallback to $HOME.
	assert.NoDirExists(t, filepath.Join(home, ".gordon", "run"))
}

func TestEnsureRuntimeDirNoFallbackOnCreateError(t *testing.T) {
	// An overlong XDG_RUNTIME_DIR component makes ancestor inspection fail
	// with ENAMETOOLONG, which is not ErrUnsafePath. The preferred candidate
	// must fail immediately rather than silently falling back to $HOME.
	base := t.TempDir()
	longName := make([]byte, 5000)
	for i := range longName {
		longName[i] = 'x'
	}
	home := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(base, string(longName)))
	t.Setenv("HOME", home)

	_, err := EnsureRuntimeDir()
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrUnsafePath)
	assert.NoDirExists(t, filepath.Join(home, ".gordon", "run"))
}

func TestEnsureDirRefusesSymlinkAncestor(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(target, link))

	dir := filepath.Join(link, "gordon")
	err := EnsureDir(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
	// Nothing must be created through the symlinked ancestor.
	assert.NoDirExists(t, filepath.Join(target, "gordon"))
	assert.NoFileExists(t, dir)
}

func TestEnsureDirTightensPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gordon")
	require.NoError(t, os.Mkdir(dir, 0o755))

	require.NoError(t, EnsureDir(dir))

	fi, err := os.Lstat(dir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), fi.Mode().Perm())
}

func TestEnsureDirRejectsSymlinkDirectory(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "gordon")
	require.NoError(t, os.Symlink(target, link))

	err := EnsureDir(link)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestEnsureDirRejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gordon")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	err := EnsureDir(path)
	require.Error(t, err)
}

func TestEnsureDirRejectsForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to change directory ownership")
	}
	dir := filepath.Join(t.TempDir(), "gordon")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.Chown(dir, 65534, 65534))

	err := EnsureDir(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestListenCreatesOwnerOnlySocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gordon")

	ln, info, err := Listen(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	path := SocketPath(dir)
	fi, err := os.Lstat(path)
	require.NoError(t, err)
	assert.NotZero(t, fi.Mode()&os.ModeSocket)
	assert.Equal(t, fs.FileMode(0o600), fi.Mode().Perm())
	assert.True(t, os.SameFile(info, fi))
	require.NoError(t, ValidateSocket(path))
}

func TestListenRefusesActiveListener(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gordon")

	ln, _, err := Listen(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	_, _, err = Listen(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrActiveListener)
	assert.FileExists(t, SocketPath(dir))
}

func TestListenRemovesStaleOwnedSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gordon")
	require.NoError(t, EnsureDir(dir))

	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: SocketPath(dir), Net: "unix"})
	require.NoError(t, err)
	stale.SetUnlinkOnClose(false)
	require.NoError(t, stale.Close())
	require.FileExists(t, SocketPath(dir))

	ln, _, err := Listen(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	assert.FileExists(t, SocketPath(dir))
}

func TestListenRefusesRegularFileCollision(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gordon")
	require.NoError(t, EnsureDir(dir))
	path := SocketPath(dir)
	require.NoError(t, os.WriteFile(path, []byte("not a socket"), 0o600))

	_, _, err := Listen(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
	// The unknown file is never deleted or replaced.
	assert.FileExists(t, path)
}

func TestListenRefusesSymlinkSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gordon")
	require.NoError(t, EnsureDir(dir))
	realDir := t.TempDir()
	real, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(realDir, "real.sock"), Net: "unix"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = real.Close() })

	require.NoError(t, os.Symlink(filepath.Join(realDir, "real.sock"), SocketPath(dir)))

	_, _, err = Listen(dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestValidateSocketRejectsGroupAccessibleSocket(t *testing.T) {
	dir := t.TempDir()
	ln, _, err := Listen(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	require.NoError(t, os.Chmod(SocketPath(dir), 0o660))
	err = ValidateSocket(SocketPath(dir))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
}

func TestRemoveOwnedSocketRemovesBoundSocket(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gordon")
	ln, info, err := Listen(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	require.NoError(t, RemoveOwnedSocket(SocketPath(dir), info))
	assert.NoFileExists(t, SocketPath(dir))
}

func TestRemoveOwnedSocketLeavesReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gordon")
	ln, info, err := Listen(dir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	path := SocketPath(dir)
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, []byte("replacement"), 0o600))

	err = RemoveOwnedSocket(path, info)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnsafePath)
	assert.FileExists(t, path)
}

func TestRemoveOwnedSocketIgnoresMissingPath(t *testing.T) {
	assert.NoError(t, RemoveOwnedSocket(filepath.Join(t.TempDir(), "absent.sock"), nil))
}

func TestClientDirCandidatesOrderAndDedupe(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	uid := strconv.Itoa(os.Getuid())
	systemDir := filepath.Join("/run/user", uid, "gordon")

	t.Run("xdg coincides with the systemd default", func(t *testing.T) {
		t.Setenv("XDG_RUNTIME_DIR", filepath.Join("/run/user", uid))
		assert.Equal(t, []string{systemDir, filepath.Join(home, ".gordon", "run")}, ClientDirCandidates())
	})

	t.Run("xdg differs from the systemd default", func(t *testing.T) {
		xdg := t.TempDir()
		t.Setenv("XDG_RUNTIME_DIR", xdg)
		assert.Equal(t, []string{
			filepath.Join(xdg, "gordon"),
			systemDir,
			filepath.Join(home, ".gordon", "run"),
		}, ClientDirCandidates())
	})
}

func TestListenRefusesForeignOwnedSocketDirectory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to change directory ownership")
	}
	dir := filepath.Join(t.TempDir(), "gordon")
	require.NoError(t, os.Mkdir(dir, 0o700))
	require.NoError(t, os.Chown(dir, 65534, 65534))

	_, _, err := Listen(dir)
	require.Error(t, err)
}
