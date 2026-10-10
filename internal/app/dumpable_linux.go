//go:build linux

package app

import "golang.org/x/sys/unix"

// disableDumpable marks the daemon non-dumpable: no core dumps, and other
// processes of the same UID can no longer read /proc/<pid>/environ, mem,
// or fds, nor ptrace it. Root keeps that access.
func disableDumpable() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
