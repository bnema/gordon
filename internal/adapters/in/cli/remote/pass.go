package remote

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"

	"github.com/bnema/gordon/internal/domain"
)

var (
	passAvailableOnce   sync.Once
	passAvailableResult bool
	passWarnOnce        sync.Once
	validRemoteName     = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
)

func validateRemoteName(name string) error {
	if name == "" {
		return domain.ErrEmptyRemoteName
	}
	if !validRemoteName.MatchString(name) {
		return fmt.Errorf("%w (allowed: a-z, A-Z, 0-9, '.', '_', '-')", domain.ErrInvalidRemoteNameChar)
	}
	if strings.Contains(name, "..") {
		return domain.ErrConsecutiveDots
	}
	return nil
}

func passAvailable() bool {
	passAvailableOnce.Do(func() {
		path, err := exec.LookPath("pass")
		if err != nil || path == "" {
			passAvailableResult = false
			return
		}

		cmd := exec.Command("pass", "ls") //nolint:gosec // pass binary name is constant
		if err := cmd.Run(); err != nil {
			passAvailableResult = false
			return
		}

		passAvailableResult = true
	})

	return passAvailableResult
}

func passTokenPath(name string) string {
	return "gordon/remotes/" + name + "/token"
}

func passReadToken(name string) (string, error) {
	if err := validateRemoteName(name); err != nil {
		return "", err
	}
	if !passAvailable() {
		return "", domain.ErrPassUnavailable
	}

	cmd := exec.Command("pass", "show", passTokenPath(name)) //nolint:gosec // pass binary name is constant; remote names are validated
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pass show failed: %w: %s", err, stderr.String())
	}

	return strings.TrimSpace(stdout.String()), nil
}

func passWriteToken(name, token string) error {
	if err := validateRemoteName(name); err != nil {
		return err
	}
	if !passAvailable() {
		return domain.ErrPassUnavailable
	}

	cmd := exec.Command("pass", "insert", "--multiline", passTokenPath(name)) //nolint:gosec // pass binary name is constant; remote names are validated
	cmd.Stdin = strings.NewReader(token + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pass insert failed: %w: %s", err, stderr.String())
	}

	return nil
}

func warnPassUnavailable() {
	passWarnOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "Warning: 'pass' not available. Storing token in plaintext config. Consider installing pass (https://www.passwordstore.org/) for secure token storage.\n")
	})
}
