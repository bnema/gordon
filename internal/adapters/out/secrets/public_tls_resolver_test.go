package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

func TestPublicTLSResolverPrefersPass(t *testing.T) {
	env := map[string]string{
		"GORDON_CLOUDFLARE_API_TOKEN": "env-token",
	}
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return " pass-token\n", nil
		},
		EnvLookup: func(key string) string {
			return env[key]
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pass-token", sv.Value)
	assert.Equal(t, domain.ACMETokenSourcePass, sv.Source)
}

func TestPublicTLSResolverFallsBackToTokenFile(t *testing.T) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "cf-token")
	err := os.WriteFile(tmpFile, []byte("file-token\n"), 0644)
	require.NoError(t, err)

	env := map[string]string{
		"GORDON_CLOUDFLARE_API_TOKEN_FILE": tmpFile,
	}
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return "", os.ErrNotExist
		},
		EnvLookup: func(key string) string {
			return env[key]
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "file-token", sv.Value)
	assert.Equal(t, domain.ACMETokenSourceFile, sv.Source)
}

func TestPublicTLSResolverFallsBackToEnv(t *testing.T) {
	env := map[string]string{
		"GORDON_CLOUDFLARE_API_TOKEN": "env-token",
	}
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return "", os.ErrNotExist
		},
		EnvLookup: func(key string) string {
			return env[key]
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-token", sv.Value)
	assert.Equal(t, domain.ACMETokenSourceEnv, sv.Source)
}

func TestPublicTLSResolverNoToken(t *testing.T) {
	env := map[string]string{}
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return "", os.ErrNotExist
		},
		EnvLookup: func(key string) string {
			return env[key]
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.NoError(t, err)
	assert.Empty(t, sv.Value)
	assert.Equal(t, domain.ACMETokenSourceNone, sv.Source)
}

func TestPublicTLSResolverPassErrorWithoutFallback(t *testing.T) {
	expectedErr := errors.New("pass show failed")
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return "", expectedErr
		},
		EnvLookup: func(key string) string {
			return ""
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolve pass token")
	assert.Contains(t, err.Error(), expectedErr.Error())
	assert.Empty(t, sv.Value)
	assert.Equal(t, domain.ACMETokenSourceNone, sv.Source)
}

func TestPublicTLSResolverPassErrorWithEnvFallback(t *testing.T) {
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("pass not available")
		},
		EnvLookup: func(key string) string {
			if key == "GORDON_CLOUDFLARE_API_TOKEN" {
				return "env-token"
			}
			return ""
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-token", sv.Value)
	assert.Equal(t, domain.ACMETokenSourceEnv, sv.Source)
}

func TestPublicTLSResolverTokenFileErrorWithoutFallback(t *testing.T) {
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return "", os.ErrNotExist
		},
		EnvLookup: func(key string) string {
			if key == "GORDON_CLOUDFLARE_API_TOKEN_FILE" {
				return "/nonexistent/token-file"
			}
			return ""
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read token file")
	assert.Empty(t, sv.Value)
	assert.Equal(t, domain.ACMETokenSourceNone, sv.Source)
}

func TestPublicTLSResolverTokenFileErrorWithEnvFallback(t *testing.T) {
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return "", os.ErrNotExist
		},
		EnvLookup: func(key string) string {
			if key == "GORDON_CLOUDFLARE_API_TOKEN_FILE" {
				return "/nonexistent/token-file"
			}
			if key == "GORDON_CLOUDFLARE_API_TOKEN" {
				return "env-token"
			}
			return ""
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-token", sv.Value)
	assert.Equal(t, domain.ACMETokenSourceEnv, sv.Source)
}

func TestPublicTLSResolverEmptyPassValueFallback(t *testing.T) {
	// Empty string from pass (no error) should fall through to other sources
	resolver := NewPublicTLSResolver(PublicTLSResolverConfig{
		PassLookup: func(_ context.Context, _ string) (string, error) {
			return "", nil // no error but empty value
		},
		EnvLookup: func(key string) string {
			if key == "GORDON_CLOUDFLARE_API_TOKEN" {
				return "env-token"
			}
			return ""
		},
	})

	sv, err := resolver.ResolveCloudflareToken(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "env-token", sv.Value)
	assert.Equal(t, domain.ACMETokenSourceEnv, sv.Source)
}

// Ensure PublicTLSResolver implements out.SecretResolver at compile time.
var _ out.SecretResolver = (*PublicTLSResolver)(nil)
