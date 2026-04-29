package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

const defaultCloudflareTokenPassName = "gordon/cloudflare/api-token"

// Package-level disabled logger and pass provider reused across calls.
var disabledLogger = zerowrap.New(zerowrap.Config{Level: "disabled"})
var defaultPassProvider = NewPassProvider(disabledLogger)

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
		if data, err := readFileWithTimeout(ctx, filePath, 5*time.Second); err == nil {
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

	return defaultPassProvider.GetSecret(ctx, name)
}

// readFileWithTimeout reads a file with a context-aware deadline.
// It runs os.ReadFile in a goroutine and selects on context expiry.
// If the context has a deadline, it is used; otherwise a timeout is applied.
func readFileWithTimeout(ctx context.Context, path string, timeout time.Duration) ([]byte, error) {
	readCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	type result struct {
		data []byte
		err  error
	}

	ch := make(chan result, 1)
	go func() {
		// os.ReadFile is not context-cancellable. The buffered channel ensures
		// this goroutine can finish without blocking if readCtx expires first;
		// for local config files this trade-off is acceptable, though reads on
		// slow NFS/FUSE mounts may continue after cancellation.
		data, err := os.ReadFile(path)
		select {
		case ch <- result{data: data, err: err}:
		case <-readCtx.Done():
		}
	}()

	select {
	case <-readCtx.Done():
		return nil, readCtx.Err()
	case r := <-ch:
		return r.data, r.err
	}
}
