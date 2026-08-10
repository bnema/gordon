package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func readProtectedSecretFile(path string) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open secret file %s: %w", path, err)
	}
	if fd < 0 {
		return "", fmt.Errorf("open secret file %s: invalid file descriptor %d", path, fd)
	}
	//nolint:gosec // G115: fd is validated non-negative and originates from unix.Open.
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return "", fmt.Errorf("open secret file %s: invalid file descriptor", path)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat secret file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must not be accessible by group or others (use chmod 600)", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return value, nil
}
