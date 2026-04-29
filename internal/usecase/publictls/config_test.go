package publictls

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func newSecretResolverMock(t *testing.T, value out.SecretValue, err error) *outmocks.MockSecretResolver {
	t.Helper()
	resolver := outmocks.NewMockSecretResolver(t)
	resolver.EXPECT().ResolveCloudflareToken(mock.Anything).Return(value, err)
	return resolver
}

func TestResolveEffectiveChallenge(t *testing.T) {
	t.Run("disabled returns auto mode with none source", func(t *testing.T) {
		cfg := Config{Enabled: false}
		ec, err := ResolveEffectiveChallenge(context.Background(), cfg, nil)
		require.NoError(t, err)
		assert.Equal(t, domain.ACMEChallengeAuto, ec.ConfiguredMode)
		assert.Equal(t, domain.ACMEChallengeAuto, ec.Mode)
		assert.Equal(t, domain.ACMETokenSourceNone, ec.TokenSource)
		assert.Equal(t, "ACME disabled", ec.Reason)
	})

	t.Run("enabled requires non-empty email", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "", TLSPort: 8443}
		_, err := ResolveEffectiveChallenge(context.Background(), cfg, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrACMEEmailRequired)
	})

	t.Run("enabled requires non-zero tls port", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "admin@example.com", TLSPort: 0}
		_, err := ResolveEffectiveChallenge(context.Background(), cfg, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrACMEChallengeInvalid)
		assert.Contains(t, err.Error(), "tls_port")
	})

	t.Run("auto with cloudflare token returns dns-01", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "admin@example.com", Challenge: "auto", TLSPort: 8443, HTTPPort: 8088}
		resolver := newSecretResolverMock(t, out.SecretValue{Value: "token", Source: domain.ACMETokenSourcePass}, nil)
		ec, err := ResolveEffectiveChallenge(context.Background(), cfg, resolver)
		require.NoError(t, err)
		assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, ec.Mode)
		assert.Equal(t, domain.ACMETokenSourcePass, ec.TokenSource)
		assert.Equal(t, "token", ec.Token)
		assert.Equal(t, domain.ACMEChallengeAuto, ec.ConfiguredMode)
	})

	t.Run("auto without token returns http-01", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "admin@example.com", Challenge: "auto", TLSPort: 8443, HTTPPort: 8088}
		resolver := newSecretResolverMock(t, out.SecretValue{Value: "", Source: domain.ACMETokenSourceNone}, nil)
		ec, err := ResolveEffectiveChallenge(context.Background(), cfg, resolver)
		require.NoError(t, err)
		assert.Equal(t, domain.ACMEChallengeHTTP01, ec.Mode)
		assert.Equal(t, domain.ACMETokenSourceNone, ec.TokenSource)
		assert.Equal(t, domain.ACMEChallengeAuto, ec.ConfiguredMode)
	})

	t.Run("forced cloudflare-dns-01 requires token", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "admin@example.com", Challenge: "cloudflare-dns-01", TLSPort: 8443, HTTPPort: 8088}
		resolver := newSecretResolverMock(t, out.SecretValue{Value: "", Source: domain.ACMETokenSourceNone}, nil)
		_, err := ResolveEffectiveChallenge(context.Background(), cfg, resolver)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrCloudflareTokenMissing)
	})

	t.Run("http-01 requires http port", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "admin@example.com", Challenge: "http-01", TLSPort: 8443, HTTPPort: 0}
		_, err := ResolveEffectiveChallenge(context.Background(), cfg, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrACMEChallengeInvalid)
	})

	t.Run("cloudflare-dns-01 preserves resolver errors", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "admin@example.com", Challenge: "cloudflare-dns-01", TLSPort: 8443}
		resolver := newSecretResolverMock(t, out.SecretValue{}, errors.New("pass failed"))
		_, err := ResolveEffectiveChallenge(context.Background(), cfg, resolver)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolve Cloudflare token")
		assert.False(t, errors.Is(err, domain.ErrCloudflareTokenMissing), "must not collapse to missing token")
	})

	t.Run("nil resolver in auto mode returns http-01", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "admin@example.com", Challenge: "auto", TLSPort: 8443, HTTPPort: 8088}
		ec, err := ResolveEffectiveChallenge(context.Background(), cfg, nil)
		require.NoError(t, err)
		assert.Equal(t, domain.ACMEChallengeHTTP01, ec.Mode)
		assert.Equal(t, domain.ACMETokenSourceNone, ec.TokenSource)
	})

	t.Run("forced cloudflare-dns-01 with nil resolver returns missing token", func(t *testing.T) {
		cfg := Config{Enabled: true, Email: "admin@example.com", Challenge: "cloudflare-dns-01", TLSPort: 8443}
		_, err := ResolveEffectiveChallenge(context.Background(), cfg, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, domain.ErrCloudflareTokenMissing)
	})
}
