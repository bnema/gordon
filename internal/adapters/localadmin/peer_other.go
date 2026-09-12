//go:build !linux

package localadmin

import (
	"context"
	"errors"
	"net"
)

// ErrPeerUIDMismatch marks a Unix socket whose listening process does not run
// as the current effective user.
var ErrPeerUIDMismatch = errors.New("local admin peer uid mismatch")

// DialContext fails closed on platforms where Gordon cannot authenticate the
// peer credentials of a Unix-domain socket. Remote administration remains
// available on these platforms.
func DialContext(context.Context, string) (net.Conn, error) {
	return nil, errors.New("authenticate local admin peer: unsupported platform")
}
