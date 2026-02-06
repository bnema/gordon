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

func TestCreateLocalDomainSecretStore_UsesFileStoreForUnsafe(t *testing.T) {
	v := viper.New()
	v.Set("auth.secrets_backend", "unsafe")

	log := zerowrap.New(zerowrap.Config{Level: "error", Format: "console"})
	store, err := createLocalDomainSecretStore(v, t.TempDir(), log)
	require.NoError(t, err)
	require.IsType(t, &domainsecrets.FileStore{}, store)
}
