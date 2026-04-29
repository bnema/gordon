package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const defaultCloudflareTokenPassName = "gordon/cloudflare/api-token"

// PublicTLSResolverConfig holds optional overrides for PublicTLSResolver.
type PublicTLSResolverConfig struct {
	PassLookup func(ctx context.Context, name string) (string, error)
	EnvLookup  func(key string) string
}

// PublicTLSResolver resolves Cloudflare API tokens using pass, file, or env.
type PublicTLSResolver struct {
	passLookup func(ctx context.Context, name string) (string, error)
	envLookup  func(key string) string
}

var _ out.SecretResolver = (*PublicTLSResolver)(nil)

// NewPublicTLSResolver creates a new PublicTLSResolver with the given config.
// Defaults PassLookup to defaultPassLookup and EnvLookup to os.Getenv.
func NewPublicTLSResolver(cfg PublicTLSResolverConfig) *PublicTLSResolver {
	r := &PublicTLSResolver{
		passLookup: defaultPassLookup,
		envLookup:  os.Getenv,
	}
	if cfg.PassLookup != nil {
		r.passLookup = cfg.PassLookup
	}
	if cfg.EnvLookup != nil {
		r.envLookup = cfg.EnvLookup
	}
	return r
}

// ResolveCloudflareToken resolves the Cloudflare API token.
// Lookup order: pass, token file env, token env.
func (r *PublicTLSResolver) ResolveCloudflareToken(ctx context.Context) (out.SecretValue, error) {
	var passErr error
	var fileErr error

	// 1. Try pass
	if token, err := r.passLookup(ctx, defaultCloudflareTokenPassName); err == nil {
		if trimmed := strings.TrimSpace(token); trimmed != "" {
			return out.SecretValue{Value: trimmed, Source: domain.ACMETokenSourcePass}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		passErr = err
	}

	// 2. Try token file env
	if filePath := r.envLookup("GORDON_CLOUDFLARE_API_TOKEN_FILE"); filePath != "" {
		if data, err := os.ReadFile(filePath); err == nil {
			if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
				return out.SecretValue{Value: trimmed, Source: domain.ACMETokenSourceFile}, nil
			}
		} else {
			fileErr = fmt.Errorf("read token file: %w", err)
		}
	}

	// 3. Try token env
	if token := r.envLookup("GORDON_CLOUDFLARE_API_TOKEN"); token != "" {
		if trimmed := strings.TrimSpace(token); trimmed != "" {
			return out.SecretValue{Value: trimmed, Source: domain.ACMETokenSourceEnv}, nil
		}
	}

	// 4. No token found — return remembered errors if any
	if passErr != nil {
		return out.SecretValue{Source: domain.ACMETokenSourceNone}, fmt.Errorf("resolve pass token: %w", passErr)
	}
	if fileErr != nil {
		return out.SecretValue{Source: domain.ACMETokenSourceNone}, fileErr
	}
	return out.SecretValue{Source: domain.ACMETokenSourceNone}, nil
}

// defaultPassLookup executes "pass show <name>" with a 2-second timeout.
// Returns the raw output with trailing newline. Never logs token values.
func defaultPassLookup(ctx context.Context, name string) (string, error) {
	// Only allow the known Cloudflare token pass name.
	if name != defaultCloudflareTokenPassName {
		return "", fmt.Errorf("pass lookup: disallowed name %q", name)
	}

	passCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	// #nosec G204 — name is validated above as the allowlisted constant
	// defaultCloudflareTokenPassName; no shell is used.
	cmd := exec.CommandContext(passCtx, "pass", "show", name)
	output, err := cmd.Output()
	if err != nil {
		if passCtx.Err() == context.DeadlineExceeded {
			return "", context.DeadlineExceeded
		}
		return "", err
	}
	return string(output), nil
}
