package env

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

type SecretProvider interface {
	GetSecret(path string) (string, error)
	Name() string
}

type PassProvider struct {
	timeout time.Duration
}

func NewPassProvider() *PassProvider {
	return &PassProvider{
		timeout: 10 * time.Second,
	}
}

func (p *PassProvider) Name() string {
	return "pass"
}

func (p *PassProvider) GetSecret(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.timeout)
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

	log.Debug().
		Str("path", path).
		Msg("Successfully retrieved secret from pass")

	return secret, nil
}

type SopsProvider struct {
	timeout time.Duration
}

func NewSopsProvider() *SopsProvider {
	return &SopsProvider{
		timeout: 10 * time.Second,
	}
}

func (s *SopsProvider) Name() string {
	return "sops"
}

func (s *SopsProvider) GetSecret(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	// Extract key from path format: file.yaml:key.nested.path
	parts := strings.SplitN(path, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("sops path must be in format 'file:key', got: %s", path)
	}

	filePath := parts[0]
	keyPath := parts[1]

	// Use sops -d --extract to get specific key
	cmd := exec.CommandContext(ctx, "sops", "-d", "--extract", fmt.Sprintf("[\"%s\"]", keyPath), filePath)
	output, err := cmd.Output()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("sops command failed: %s", string(exitError.Stderr))
		}
		return "", fmt.Errorf("failed to execute sops command: %w", err)
	}

	secret := strings.TrimSpace(string(output))
	if secret == "" {
		return "", fmt.Errorf("empty secret returned from sops for path: %s", path)
	}

	log.Debug().
		Str("path", path).
		Msg("Successfully retrieved secret from sops")

	return secret, nil
}