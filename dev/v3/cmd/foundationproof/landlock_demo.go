// landlock-demo is an explicitly test-only A1A.1 spike. It restricts its
// own filesystem view with unprivileged Landlock and then probes what stays
// usable. It proves or refutes the mechanism; it is not ingress code.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

func landlockCreate(handled uint64) (int, error) {
	attr := unix.LandlockRulesetAttr{Access_fs: handled}
	//nolint:gosec // test-only spike: raw Landlock syscall has no stdlib equivalent; no Landlock library dependency wanted.
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func landlockAllow(ruleset int, path string, access uint64) error {
	parent, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer unix.Close(parent)
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(parent)} //nolint:gosec // parent is an open(2) fd, non-negative and far below int32 range.
	//nolint:gosec // test-only spike: raw Landlock syscall has no stdlib equivalent; no Landlock library dependency wanted.
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH), uintptr(unsafe.Pointer(&rule)), unsafe.Sizeof(rule), 0, 0)
	if errno != 0 {
		return fmt.Errorf("add rule %s: %w", path, errno)
	}
	return nil
}

func landlockRestrict(ruleset int) error {
	_, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(ruleset), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func landlockDemo(dir string) error {
	if dir == "" {
		return errors.New(usage)
	}
	ipc := filepath.Join(dir, "ipc")
	secret := filepath.Join(dir, "secrets", "app-secret")
	handled := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_EXECUTE)
	fd, err := landlockCreate(handled)
	if err != nil {
		return fmt.Errorf("create ruleset: %w", err)
	}
	defer unix.Close(fd)
	// Traversal everywhere, content only where explicitly allowed.
	if err := landlockAllow(fd, "/", uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE)); err != nil {
		return err
	}
	rw := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR)
	if err := landlockAllow(fd, ipc, rw); err != nil {
		return err
	}
	if err := landlockAllow(fd, "/proc", uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_READ_DIR)); err != nil {
		return err
	}
	if err := landlockRestrict(fd); err != nil {
		return fmt.Errorf("restrict self: %w", err)
	}
	fmt.Println("landlock enforced")
	// Forbidden: canary read must fail.
	if _, err := os.ReadFile(secret); err != nil {
		fmt.Println("PASS F1 read: denied")
	} else {
		fmt.Println("FAIL F1 read: allowed")
	}
	// Forbidden: traversal link must fail.
	if _, err := os.ReadFile(filepath.Join(dir, "sockets", "link-to-secret")); err != nil {
		fmt.Println("PASS F6 read-traversal: denied")
	} else {
		fmt.Println("FAIL F6 read-traversal: allowed")
	}
	// Allowed: IPC read/write under the permitted directory.
	probe := filepath.Join(ipc, "probe")
	if err := os.WriteFile(probe, []byte("ping\n"), 0o600); err != nil {
		fmt.Println("FAIL B2 ipc-read-write: " + err.Error())
	} else if _, err := os.ReadFile(probe); err != nil {
		fmt.Println("FAIL B2 ipc-read-write: " + err.Error())
	} else {
		os.Remove(probe)
		fmt.Println("PASS B2 ipc-read-write: allowed")
	}
	// Allowed: TCP bind needs no filesystem access.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("FAIL B1 bind-tcp: " + err.Error())
	} else {
		ln.Close()
		fmt.Println("PASS B1 bind-tcp: allowed")
	}
	return nil
}
