// Package secrets implements secret provider adapters.
package secrets

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
)

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

// GetSecret retrieves a secret from pass by path.
func (p *PassProvider) GetSecret(ctx context.Context, path string) (string, error) {
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
