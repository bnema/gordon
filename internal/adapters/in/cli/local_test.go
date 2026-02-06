package cli

import (
	"testing"

	"github.com/bnema/zerowrap"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/adapters/out/domainsecrets"
	"github.com/bnema/gordon/internal/domain"
)

func TestResolveLocalSecretsBackend(t *testing.T) {
	t.Run("auth backend takes precedence", func(t *testing.T) {
		v := viper.New()
		v.Set("auth.secrets_backend", "pass")
		v.Set("secrets.backend", "unsafe")

		got := resolveLocalSecretsBackend(v)
		require.Equal(t, domain.SecretsBackendPass, got)
	})

	t.Run("falls back to legacy secrets backend key", func(t *testing.T) {
		v := viper.New()
		v.Set("secrets.backend", "pass")

		got := resolveLocalSecretsBackend(v)
		require.Equal(t, domain.SecretsBackendPass, got)
	})

	t.Run("defaults to unsafe for unknown backend", func(t *testing.T) {
		v := viper.New()
		v.Set("auth.secrets_backend", "wat")

		got := resolveLocalSecretsBackend(v)
		require.Equal(t, domain.SecretsBackendUnsafe, got)
	})
}

func TestCreateLocalDomainSecretStore_UsesPassStoreForPass(t *testing.T) {
	// pass(1) must be available on the system for this test
	if _, err := domainsecrets.NewPassStore(zerowrap.New(zerowrap.Config{Level: "error", Format: "console"})); err != nil {
		t.Skipf("pass store not available: %v", err)
	}

	v := viper.New()
	v.Set("auth.secrets_backend", "pass")

	log := zerowrap.New(zerowrap.Config{Level: "error", Format: "console"})
	store, err := createLocalDomainSecretStore(v, t.TempDir(), log)
	require.NoError(t, err)
	require.IsType(t, &domainsecrets.PassStore{}, store)
}

func TestCreateLocalDomainSecretStore_RejectsSopsBackend(t *testing.T) {
	v := viper.New()
	v.Set("auth.secrets_backend", "sops")

	log := zerowrap.New(zerowrap.Config{Level: "error", Format: "console"})
	_, err := createLocalDomainSecretStore(v, t.TempDir(), log)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sops backend not yet supported")
}

func TestCreateLocalDomainSecretStore_UsesFileStoreForUnsafe(t *testing.T) {
	v := viper.New()
	v.Set("auth.secrets_backend", "unsafe")

	log := zerowrap.New(zerowrap.Config{Level: "error", Format: "console"})
	store, err := createLocalDomainSecretStore(v, t.TempDir(), log)
	require.NoError(t, err)
	require.IsType(t, &domainsecrets.FileStore{}, store)
}
