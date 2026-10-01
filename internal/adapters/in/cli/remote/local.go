package remote

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bnema/gordon/internal/adapters/localadmin"
)

// LocalBaseURL is the synthetic origin of the Unix-socket admin client. The
// transport ignores the host and always dials the owner-only socket.
const LocalBaseURL = "http://gordon.local"

// ErrDaemonUnavailable reports that no owner-only admin socket passed
// validation, so no local daemon can be reached.
var ErrDaemonUnavailable = errors.New("daemon-unavailable")

// errLocalRedirect refuses redirects on the local socket: a redirect can never
// be a legitimate part of the owner-only admin surface.
var errLocalRedirect = errors.New("local admin client does not follow redirects")

// localSocketDirs lists the discovery candidates in daemon priority order.
// It is a variable so tests can isolate from a real daemon.
var localSocketDirs = localadmin.ClientDirCandidates

// DiscoverLocalSocket returns the first admin socket candidate, in daemon
// priority order, that is an owner-owned Unix socket without group/other
// permission bits.
func DiscoverLocalSocket() (string, error) {
	return DiscoverLocalSocketIn(localSocketDirs())
}

// DiscoverLocalSocketIn is DiscoverLocalSocket over explicit candidate
// directories. Each candidate is validated, never followed through symlinks.
func DiscoverLocalSocketIn(dirs []string) (string, error) {
	var lastErr error
	for _, dir := range dirs {
		path := filepath.Join(dir, localadmin.SocketName)
		if _, err := os.Lstat(path); err != nil {
			lastErr = err
			continue
		}
		if err := localadmin.ValidateDir(dir); err != nil {
			return "", fmt.Errorf("%w: unsafe local admin directory: %w", ErrDaemonUnavailable, err)
		}
		if err := localadmin.ValidateSocket(path); err != nil {
			return "", fmt.Errorf("%w: unsafe local admin socket: %w", ErrDaemonUnavailable, err)
		}
		live, err := localadmin.ProbeSocket(path)
		if err != nil {
			return "", fmt.Errorf("%w: probe local admin socket: %w", ErrDaemonUnavailable, err)
		}
		if !live {
			lastErr = syscall.ECONNREFUSED
			continue
		}
		return path, nil
	}
	if lastErr == nil {
		lastErr = fs.ErrNotExist
	}
	return "", fmt.Errorf("%w: no owner-only admin socket found: %w", ErrDaemonUnavailable, lastErr)
}

// NewLocalClient discovers the daemon's owner-only admin socket and returns a
// client bound to it. It returns ErrDaemonUnavailable when no daemon is
// reachable; callers must not fall back to local writes.
func NewLocalClient() (*Client, error) {
	socketPath, err := DiscoverLocalSocket()
	if err != nil {
		return nil, err
	}
	return NewLocalClientForSocket(socketPath), nil
}

// NewLocalClientForSocket returns a client whose transport dials only the
// given Unix socket. It sends no Authorization header and never exchanges a
// token, so it works with auth.enabled=false.
func NewLocalClientForSocket(socketPath string) *Client {
	transport := &http.Transport{
		// Never consult environment proxies for the local socket. DialContext
		// authenticates SO_PEERCRED before the transport can write request bytes.
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return localadmin.DialContext(ctx, socketPath)
		},
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errLocalRedirect
		},
	}

	return NewClient(LocalBaseURL, WithHTTPClient(httpClient))
}
