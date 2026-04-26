package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/bnema/gordon/internal/domain"
)

func TestBuildAuthConfig_UsesConfigEnabledFlag(t *testing.T) {
	t.Setenv(TokenSecretEnvVar, "test-token-secret-that-is-at-least-32-bytes-long")

	cfg := Config{}
	cfg.Auth.Enabled = false
	cfg.Auth.Type = "token"

	authCfg, err := buildAuthConfig(context.Background(), cfg, domain.AuthTypeToken, domain.SecretsBackendUnsafe, t.TempDir(), zerowrap.Default())
	require.NoError(t, err)
	assert.False(t, authCfg.Enabled, "expected auth config enabled=false")
}

func TestResolveSecretsBackend_RejectsMissingBackend(t *testing.T) {
	_, err := resolveSecretsBackend("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth.secrets_backend is required")
}

func TestResolveSecretsBackend_RejectsUnknownBackend(t *testing.T) {
	_, err := resolveSecretsBackend("vault")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unsupported auth.secrets_backend "vault"`)
}

func TestResolveAuthType_RejectsPassword(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Type = "password"
	_, err := resolveAuthType(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported auth.type")
}

func TestResolveAuthType_AcceptsToken(t *testing.T) {
	cfg := Config{}
	cfg.Auth.Type = "token"
	authType, err := resolveAuthType(cfg)
	require.NoError(t, err)
	assert.Equal(t, domain.AuthTypeToken, authType)
}

func TestResolveAuthType_AcceptsEmpty(t *testing.T) {
	cfg := Config{}
	authType, err := resolveAuthType(cfg)
	require.NoError(t, err)
	assert.Equal(t, domain.AuthTypeToken, authType)
}

func TestLoadConfig_DoesNotDefaultSecretsBackendToUnsafe(t *testing.T) {
	v := viper.New()

	err := loadConfig(v, "")
	require.NoError(t, err)
	assert.Empty(t, v.GetString("auth.secrets_backend"), "expected empty default for auth.secrets_backend")
}

func TestLoadSecretUnsafeRejectsTraversalAndAbsolutePaths(t *testing.T) {
	dataDir := t.TempDir()
	secretsDir := filepath.Join(dataDir, "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "token"), []byte("safe-secret"), 0600))
	outside := filepath.Join(dataDir, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("outside-secret"), 0600))

	for _, path := range []string{"../outside", filepath.Join(dataDir, "outside")} {
		t.Run(path, func(t *testing.T) {
			_, err := loadSecret(context.Background(), domain.SecretsBackendUnsafe, path, dataDir, zerowrap.Default())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid secret path")
		})
	}
}

func TestLoadSecretUnsafeAllowsRelativePathUnderSecretsDir(t *testing.T) {
	dataDir := t.TempDir()
	secretsDir := filepath.Join(dataDir, "secrets", "auth")
	require.NoError(t, os.MkdirAll(secretsDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "token"), []byte("safe-secret"), 0600))

	secret, err := loadSecret(context.Background(), domain.SecretsBackendUnsafe, "auth/token", dataDir, zerowrap.Default())
	require.NoError(t, err)
	assert.Equal(t, "safe-secret", secret)
}

func TestLoadSecretUnsafeRejectsMissingCandidate(t *testing.T) {
	dataDir := t.TempDir()
	secretsDir := filepath.Join(dataDir, "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0700))

	_, err := loadSecret(context.Background(), domain.SecretsBackendUnsafe, "missing", dataDir, zerowrap.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read secret file")
}

func TestLoadSecretUnsafeRejectsIntermediateSymlinkEscape(t *testing.T) {
	dataDir := t.TempDir()
	secretsDir := filepath.Join(dataDir, "secrets")
	outsideDir := filepath.Join(dataDir, "outside")
	require.NoError(t, os.MkdirAll(secretsDir, 0700))
	require.NoError(t, os.MkdirAll(outsideDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(outsideDir, "token"), []byte("outside-secret"), 0600))
	require.NoError(t, os.Symlink(outsideDir, filepath.Join(secretsDir, "linkdir")))

	_, err := loadSecret(context.Background(), domain.SecretsBackendUnsafe, "linkdir/token", dataDir, zerowrap.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open secret path component")
}

func TestLoadSecretUnsafeRejectsNonRegularFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix mkfifo is not available on windows")
	}
	dataDir := t.TempDir()
	secretsDir := filepath.Join(dataDir, "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0700))
	require.NoError(t, unix.Mkfifo(filepath.Join(secretsDir, "token"), 0600))

	_, err := loadSecret(context.Background(), domain.SecretsBackendUnsafe, "token", dataDir, zerowrap.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secret must be a regular file")
}

func TestLoadSecretUnsafeRejectsSymlinkEscape(t *testing.T) {
	dataDir := t.TempDir()
	secretsDir := filepath.Join(dataDir, "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0700))
	outside := filepath.Join(dataDir, "outside")
	require.NoError(t, os.WriteFile(outside, []byte("outside-secret"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(secretsDir, "token")))

	_, err := loadSecret(context.Background(), domain.SecretsBackendUnsafe, "token", dataDir, zerowrap.Default())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read secret file")
}
