package secrets

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bnema/zerowrap"
)

// sopsKeyPathRegex validates sops key paths (alphanumeric, dots, underscores, hyphens, brackets).
var sopsKeyPathRegex = regexp.MustCompile(`^[a-zA-Z0-9._\-\[\]]+$`)

// SopsProvider implements the SecretProvider interface using sops.
type SopsProvider struct {
	timeout time.Duration
	log     zerowrap.Logger
}

// NewSopsProvider creates a new sops provider.
func NewSopsProvider(log zerowrap.Logger) *SopsProvider {
	return &SopsProvider{
		timeout: 10 * time.Second,
		log:     log,
	}
}

// Name returns the provider name.
func (s *SopsProvider) Name() string {
	return "sops"
}

// GetSecret retrieves a secret from a sops-encrypted file.
// The path format is "file.yaml:key.nested.path".
func (s *SopsProvider) GetSecret(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// Extract key from path format: file.yaml:key.nested.path
	parts := strings.SplitN(path, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("sops path must be in format 'file:key', got: %s", path)
	}

	filePath := parts[0]
	keyPath := parts[1]

	// Validate file path to prevent command injection
	cleanPath := filepath.Clean(filePath)
	if strings.Contains(cleanPath, "..") {
		return "", fmt.Errorf("invalid file path: path traversal not allowed")
	}

	// Validate key path to prevent command injection
	if !sopsKeyPathRegex.MatchString(keyPath) {
		return "", fmt.Errorf("invalid key path: must contain only alphanumeric characters, dots, underscores, hyphens, and brackets")
	}

	// Use sops -d --extract to get specific key
	// #nosec G204 - filePath and keyPath are validated above
	cmd := exec.CommandContext(ctx, "sops", "-d", "--extract", fmt.Sprintf("[\"%s\"]", keyPath), cleanPath)
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

	s.log.Debug().
		Str(zerowrap.FieldLayer, "adapter").
		Str(zerowrap.FieldAdapter, "secrets").
		Str("provider", "sops").
		Str("path", path).
		Msg("successfully retrieved secret from sops")

	return secret, nil
}

// IsAvailable checks if sops is available in the system.
func (s *SopsProvider) IsAvailable() bool {
	cmd := exec.Command("sops", "--version")
	err := cmd.Run()
	return err == nil
}
