// Package appsecrets implements out.SecretWriter on pass for v2.50 app
// secrets at gordon/apps/<app>/<service>/<name>. Values never pass
// through app state, diffs, logs, or backup metadata — only this
// explicit write path and the deployment-time read path carry them.
package appsecrets

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// Store writes app secret values through the pass CLI.
type Store struct {
	timeout time.Duration
	log     zerowrap.Logger
}

// NewStore creates a pass-backed app secret writer.
func NewStore(log zerowrap.Logger) *Store {
	return &Store{timeout: 30 * time.Second, log: log}
}

// SetSecret implements out.SecretWriter.
func (s *Store) SetSecret(ctx context.Context, path, value string) error {
	if err := validateAppSecretPath(path); err != nil {
		return err
	}
	if strings.Contains(value, "\n") {
		return fmt.Errorf("appsecrets: secret value must not contain newlines: %w", domain.ErrInvalidAppSpec)
	}
	if len(value) == 0 || len(value) > domain.MaxAppEnvValueLen {
		return fmt.Errorf("appsecrets: secret value must be 1-%d bytes: %w", domain.MaxAppEnvValueLen, domain.ErrInvalidAppSpec)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "pass", "insert", "-m", "-f", path) //nolint:gosec // binary is constant ("pass"); path validated below
	cmd.Stdin = strings.NewReader(value)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("appsecrets: pass insert failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	s.log.Info().Str("path", path).Msg("appsecrets: secret written")
	return nil
}

// DeleteSecret implements out.SecretWriter.
func (s *Store) DeleteSecret(ctx context.Context, path string) error {
	if err := validateAppSecretPath(path); err != nil {
		return err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	cmd := exec.CommandContext(timeoutCtx, "pass", "rm", "-f", path) //nolint:gosec // binary is constant ("pass"); path validated below
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("appsecrets: pass rm failed: %s: %w", strings.TrimSpace(string(output)), err)
	}
	s.log.Info().Str("path", path).Msg("appsecrets: secret deleted")
	return nil
}

// validateAppSecretPath constrains writes to the app secret namespace.
func validateAppSecretPath(path string) error {
	rest, ok := strings.CutPrefix(path, "gordon/apps/")
	if !ok || rest == "" {
		return fmt.Errorf("appsecrets: path %q must live under gordon/apps/: %w", path, domain.ErrInvalidAppSpec)
	}
	for _, part := range strings.Split(rest, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, " \t\n\r\\\"'$`*?[]{}()|&;<>!") {
			return fmt.Errorf("appsecrets: path %q has invalid segment: %w", path, domain.ErrInvalidAppSpec)
		}
	}
	return nil
}
