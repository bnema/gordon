// Package localadmin owns the owner-only Unix socket policy shared by the
// Gordon daemon and the local CLI: runtime directory discovery, strict
// filesystem validation, and safe listener lifecycle.
//
// It deliberately depends only on the standard library so the composition
// root (internal/app) and the incoming CLI adapter can share it without
// creating an adapter-to-application dependency.
package localadmin

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// SocketName is the fixed filename of the owner-only admin socket.
const SocketName = "admin.sock"

// DirPermissions is the required mode of the runtime directory.
const DirPermissions fs.FileMode = 0o700

// SocketPermissions is the required mode of the admin socket.
const SocketPermissions fs.FileMode = 0o600

// OtherPermissions is the union of permission bits that must never be set.
const OtherPermissions fs.FileMode = 0o077

// staleDialTimeout bounds the liveness probe performed before removing a
// stale socket path.
const staleDialTimeout = 500 * time.Millisecond

// ErrUnsafePath marks a path that failed ownership, type, or mode validation.
var ErrUnsafePath = errors.New("unsafe local admin path")

// ErrActiveListener marks a socket that already has a live listener.
var ErrActiveListener = errors.New("local admin socket already has an active listener")

// SocketPath joins dir with the fixed socket filename.
func SocketPath(dir string) string {
	return filepath.Join(dir, SocketName)
}

// EnsureRuntimeDir returns, creates, and validates the daemon runtime
// directory: $XDG_RUNTIME_DIR/gordon when set, otherwise $HOME/.gordon/run.
// A set XDG_RUNTIME_DIR must be absolute; any failure to ensure the preferred
// directory fails closed and never falls back to $HOME, since a silent
// fallback would change daemon identity.
func EnsureRuntimeDir() (string, error) {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		if !filepath.IsAbs(xdg) {
			return "", fmt.Errorf("%w: XDG_RUNTIME_DIR is not absolute: %q", ErrUnsafePath, xdg)
		}
		dir := filepath.Join(xdg, "gordon")
		if err := EnsureDir(dir); err != nil {
			return "", err
		}
		return dir, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".gordon", "run")
	if err := EnsureDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// ClientDirCandidates lists CLI discovery candidates in daemon priority
// order: $XDG_RUNTIME_DIR/gordon, /run/user/<uid>/gordon, then
// $HOME/.gordon/run. Directories are not created.
func ClientDirCandidates() []string {
	var candidates []string

	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "gordon"))
	}

	systemDir := filepath.Join("/run/user", fmt.Sprintf("%d", os.Getuid()), "gordon")
	if len(candidates) == 0 || candidates[0] != systemDir {
		candidates = append(candidates, systemDir)
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".gordon", "run"))
	}

	return candidates
}

// EnsureDir creates dir with owner-only permissions when missing, tightens an
// existing directory, and then validates it as an owner-owned real directory
// without group/other permission bits.
func EnsureDir(dir string) error {
	// Validate the existing ancestor chain before creating anything so
	// MkdirAll never creates directories through a symlinked, foreign, or
	// otherwise unsafe ancestor.
	if err := validateExistingAncestorChain(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, DirPermissions); err != nil {
		return fmt.Errorf("create local admin runtime directory: %w", err)
	}

	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect local admin runtime directory: %w", err)
	}
	// Refuse symlinks, non-directories, and foreign owners before any chmod so
	// a hostile path never has its target modified through a link.
	if err := validateDirIdentity(fi, dir); err != nil {
		return err
	}
	if fi.Mode().Perm()&OtherPermissions != 0 {
		if err := os.Chmod(dir, DirPermissions); err != nil {
			return fmt.Errorf("restrict local admin runtime directory: %w", err)
		}
	}
	return ValidateDir(dir)
}

// ValidateDir reports whether dir is an owner-owned real directory without
// group/other permission bits.
func ValidateDir(dir string) error {
	if err := validateParentChain(dir); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect local admin runtime directory: %w", err)
	}
	if err := validateDirIdentity(fi, dir); err != nil {
		return err
	}
	if fi.Mode().Perm()&OtherPermissions != 0 {
		return fmt.Errorf("%w: runtime directory %s has group/other permissions %04o", ErrUnsafePath, dir, fi.Mode().Perm())
	}
	return nil
}

// validateExistingAncestorChain validates every existing ancestor of dir
// before MkdirAll runs, so missing directories are never created through a
// symlinked, non-directory, or foreign-writable ancestor. Missing ancestors
// are skipped: they will be created by MkdirAll once the existing chain is
// known safe.
func validateExistingAncestorChain(dir string) error {
	for current := filepath.Dir(filepath.Clean(dir)); ; current = filepath.Dir(current) {
		fi, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				parent := filepath.Dir(current)
				if parent == current {
					return nil
				}
				continue
			}
			return fmt.Errorf("inspect local admin path component %s: %w", current, err)
		}
		if err := validatePathComponent(fi, current); err != nil {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func validatePathComponent(fi os.FileInfo, path string) error {
	return validatePathComponentForUID(fi, path, uint32(os.Geteuid())) // #nosec G115 -- Unix effective UIDs fit uid_t.
}

func validatePathComponentForUID(fi os.FileInfo, path string, euid uint32) error {
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return fmt.Errorf("%w: path component %s is not a real directory", ErrUnsafePath, path)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: cannot determine owner of %s", ErrUnsafePath, path)
	}
	if st.Uid != 0 && st.Uid != euid {
		return fmt.Errorf("%w: path component %s is owned by uid %d, not root or %d", ErrUnsafePath, path, st.Uid, euid)
	}
	writable := fi.Mode().Perm()&0o022 != 0
	stickyRoot := st.Uid == 0 && fi.Mode()&os.ModeSticky != 0
	if writable && !stickyRoot {
		return fmt.Errorf("%w: path component %s is replaceable", ErrUnsafePath, path)
	}
	return nil
}

// validateDirIdentity checks the properties that no chmod can repair: a symlink
// target, a non-directory, or another user's path.
func validateParentChain(dir string) error {
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		fi, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect local admin path component %s: %w", current, err)
		}
		if err := validatePathComponent(fi, current); err != nil {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func validateDirIdentity(fi os.FileInfo, dir string) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: runtime directory %s is a symlink", ErrUnsafePath, dir)
	}
	if !fi.IsDir() {
		return fmt.Errorf("%w: runtime directory %s is not a directory", ErrUnsafePath, dir)
	}
	if err := validateOwner(fi); err != nil {
		return fmt.Errorf("runtime directory %s: %w", dir, err)
	}
	return nil
}

// ValidateSocket reports whether path is an owner-owned Unix socket without
// group/other permission bits and without a symlink at the final component.
func ValidateSocket(path string) error {
	if err := validateParentChain(filepath.Dir(path)); err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validateSocketFile(fi, path)
}

// Listen binds the owner-only admin socket inside dir and returns the
// listener plus the identity of the file it created.
//
// It fails closed: the directory must be owner-owned, a real directory, and
// owner-only; an existing path must already be an owner-owned socket; and an
// existing socket is removed only after a bounded dial proves no listener is
// active. Any other existing path is never replaced.
func Listen(dir string) (*net.UnixListener, os.FileInfo, error) {
	if err := EnsureDir(dir); err != nil {
		return nil, nil, err
	}

	path := SocketPath(dir)
	if err := clearStale(path); err != nil {
		return nil, nil, err
	}

	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, nil, fmt.Errorf("bind local admin socket: %w", err)
	}
	// Keep unlink-on-close under our control so shutdown removes the socket
	// only after verifying it is still the file we bound.
	ln.SetUnlinkOnClose(false)

	bound, err := os.Lstat(path)
	if err != nil {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("inspect newly bound local admin socket: %w", err)
	}
	if err := os.Chmod(path, SocketPermissions); err != nil {
		return nil, nil, cleanupListen(ln, path, bound, fmt.Errorf("restrict local admin socket: %w", err))
	}

	fi, err := os.Lstat(path)
	if err != nil {
		return nil, nil, cleanupListen(ln, path, bound, fmt.Errorf("inspect local admin socket: %w", err))
	}
	if !os.SameFile(bound, fi) {
		return nil, nil, cleanupListen(ln, path, bound, fmt.Errorf("%w: local admin socket was replaced during setup", ErrUnsafePath))
	}
	if err := validateSocketFile(fi, path); err != nil {
		return nil, nil, cleanupListen(ln, path, bound, err)
	}

	return ln, fi, nil
}

// RemoveOwnedSocket removes path only when it is still the same file that was
// bound. A replaced path is left untouched and reported as unsafe.
func RemoveOwnedSocket(path string, bound os.FileInfo) error {
	if bound == nil {
		return nil
	}
	current, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local admin socket for removal: %w", err)
	}
	if !os.SameFile(bound, current) {
		return fmt.Errorf("%w: local admin socket %s was replaced; not removing it", ErrUnsafePath, path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove local admin socket: %w", err)
	}
	return nil
}

func cleanupListen(ln *net.UnixListener, path string, bound os.FileInfo, cause error) error {
	_ = ln.Close()
	_ = RemoveOwnedSocket(path, bound)
	return cause
}

func validateSocketFile(fi os.FileInfo, path string) error {
	if err := validateSocketIdentity(fi, path); err != nil {
		return err
	}
	if fi.Mode().Perm()&OtherPermissions != 0 {
		return fmt.Errorf("%w: %s has group/other permissions %04o", ErrUnsafePath, path, fi.Mode().Perm())
	}
	return nil
}

// validateSocketIdentity checks only the properties that must hold before a
// stale path may be removed: a non-symlink Unix socket owned by this euid.
// Callers that consume the socket additionally require mode 0600.
func validateSocketIdentity(fi os.FileInfo, path string) error {
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s is a symlink", ErrUnsafePath, path)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: %s is not a Unix socket", ErrUnsafePath, path)
	}
	if err := validateOwner(fi); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func validateOwner(fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%w: cannot determine file owner", ErrUnsafePath)
	}
	if int64(st.Uid) != int64(os.Geteuid()) {
		return fmt.Errorf("%w: owned by uid %d, not %d", ErrUnsafePath, st.Uid, os.Geteuid())
	}
	return nil
}

// clearStale removes an existing socket path only when it is an owner-owned
// socket with no active listener. Everything else fails closed.
func clearStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect local admin socket: %w", err)
	}
	if err := validateSocketIdentity(fi, path); err != nil {
		return fmt.Errorf("refusing to replace local admin path: %w", err)
	}

	live, err := listening(path)
	if err != nil {
		return err
	}
	if live {
		return fmt.Errorf("%w at %s", ErrActiveListener, path)
	}

	current, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reinspect stale local admin socket: %w", err)
	}
	if !os.SameFile(fi, current) {
		return fmt.Errorf("%w: local admin socket changed during stale check", ErrUnsafePath)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove stale local admin socket: %w", err)
	}
	return nil
}

// ProbeSocket reports whether a validated socket has a live listener.
func ProbeSocket(path string) (bool, error) {
	return listening(path)
}

// listening dials path with a bounded timeout. A live listener reports true;
// an explicit no-listener error reports false. Any other dial failure is
// returned so an unexpected condition never leads to removing a path.
func listening(path string) (bool, error) {
	conn, err := net.DialTimeout("unix", path, staleDialTimeout)
	if err == nil {
		_ = conn.Close()
		return true, nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) || errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("probe local admin socket %s: %w", path, err)
}
