// Package secrets implements secret provider adapters.
package secrets

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"gordon/internal/domain"
)

// passPathRegex validates pass paths.
// Allows alphanumeric characters, forward slashes, underscores, dots, and hyphens.
// This prevents command injection via shell metacharacters.
var passPathRegex = regexp.MustCompile(`^[a-zA-Z0-9/_.-]+$`)

// PassProvider implements the SecretProvider interface using the pass password manager.
type PassProvider struct {
	timeout time.Duration
	log     zerowrap.Logger
}

// NewPassProvider creates a new pass provider.
func NewPassProvider(log zerowrap.Logger) *PassProvider {
	return &PassProvider{
		timeout: 10 * time.Second,
		log:     log,
	}
}

// Name returns the provider name.
func (p *PassProvider) Name() string {
	return "pass"
}

// ValidatePath validates a pass path to prevent command injection and path traversal.
// Returns an error if the path is invalid.
func ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("path cannot be empty")
	}

	// SECURITY: Validate path format to prevent command injection
	if !passPathRegex.MatchString(path) {
		return fmt.Errorf("invalid path: must contain only alphanumeric characters, /, _, ., -")
	}

	// SECURITY: Prevent path traversal
	if strings.Contains(path, "..") {
		return domain.ErrPathTraversal
	}

	// SECURITY: Reject absolute paths
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("invalid path: absolute paths not allowed")
	}

	return nil
}

// GetSecret retrieves a secret from pass by path.
func (p *PassProvider) GetSecret(ctx context.Context, path string) (string, error) {
	// SECURITY: Validate path before executing command
	if err := ValidatePath(path); err != nil {
		p.log.Warn().
			Str(zerowrap.FieldLayer, "adapter").
			Str(zerowrap.FieldAdapter, "secrets").
			Str("provider", "pass").
			Str("path", path).
			Err(err).
			Msg("rejected invalid pass path")
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "pass", "show", path)
	output, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("pass command failed: %s", string(exitError.Stderr))
		}
		return "", fmt.Errorf("failed to execute pass command: %w", err)
	}

	secret := strings.TrimSpace(string(output))
	if secret == "" {
		return "", fmt.Errorf("empty secret returned from pass for path: %s", path)
	}

	p.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "secrets").
		Str("provider", "pass").
		Str("path", path).
		Msg("successfully retrieved secret from pass")

	return secret, nil
}

// IsAvailable checks if pass is available in the system.
func (p *PassProvider) IsAvailable() bool {
	cmd := exec.Command("pass", "version")
	err := cmd.Run()
	return err == nil
}
