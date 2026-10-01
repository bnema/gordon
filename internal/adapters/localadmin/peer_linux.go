//go:build linux

package localadmin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// ErrPeerUIDMismatch marks a Unix socket whose listening process does not run
// as the current effective user.
var ErrPeerUIDMismatch = errors.New("local admin peer uid mismatch")

// DialContext connects to path and authenticates the listening process before
// returning the connection to an HTTP transport.
func DialContext(ctx context.Context, path string) (net.Conn, error) {
	euid := os.Geteuid()
	if euid < 0 {
		return nil, fmt.Errorf("authenticate local admin peer: invalid effective uid %d", euid)
	}
	return dialContextForUID(ctx, path, uint32(euid)) // #nosec G115 -- euid is non-negative and Linux uid_t is uint32.
}

func dialContextForUID(ctx context.Context, path string, expectedUID uint32) (net.Conn, error) {
	if err := ValidateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := ValidateSocket(path); err != nil {
		return nil, err
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("authenticate local admin peer: unexpected connection type %T", conn)
	}

	uid, err := peerUID(unixConn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("authenticate local admin peer: %w", err)
	}
	if uid != expectedUID {
		_ = conn.Close()
		return nil, fmt.Errorf("%w: got %d, want %d", ErrPeerUIDMismatch, uid, expectedUID)
	}
	return conn, nil
}

func peerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var (
		cred    *unix.Ucred
		sockErr error
	)
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if sockErr != nil {
		return 0, sockErr
	}
	return cred.Uid, nil
}
