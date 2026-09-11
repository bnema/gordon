package publictls

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	outmocks "github.com/bnema/gordon/internal/boundaries/out/mocks"
	"github.com/bnema/gordon/internal/domain"
)

func TestDeriveTargets_HTTP01PerRoute(t *testing.T) {
	hosts := []out.AppHost{{Host: "app.example.com"}, {Host: "registry.example.com"}}
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeHTTP01, hosts, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	assert.Equal(t, "http01-app.example.com", targets[0].ID)
	assert.Equal(t, []string{"app.example.com"}, targets[0].Names)
	assert.Equal(t, domain.ACMEChallengeHTTP01, targets[0].Challenge)
	assert.Equal(t, "http01-registry.example.com", targets[1].ID)
	assert.Equal(t, []string{"registry.example.com"}, targets[1].Names)
	assert.Equal(t, domain.ACMEChallengeHTTP01, targets[1].Challenge)
}

func TestDeriveTargets_DNS01WildcardBases(t *testing.T) {
	hosts := []out.AppHost{{Host: "app.example.com"}, {Host: "api.prod.example.com"}, {Host: "example.com"}}
	resolver := outmocks.NewMockCloudflareZoneResolver(t)
	resolver.EXPECT().FindZone(mock.Anything, mock.Anything).Return(out.CloudflareZone{Name: "example.com"}, nil).Times(3)
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeCloudflareDNS01, hosts, nil, nil, resolver)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	assert.Equal(t, "dns01-example.com", targets[0].ID)
	assert.Equal(t, []string{"example.com", "*.example.com"}, targets[0].Names)
	assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, targets[0].Challenge)
	assert.Equal(t, "dns01-prod.example.com", targets[1].ID)
	assert.Equal(t, []string{"prod.example.com", "*.prod.example.com"}, targets[1].Names)
	assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, targets[1].Challenge)
}

func TestDeriveTargets_IncludesAdditionalHosts(t *testing.T) {
	targets, err := DeriveCertificateTargets(
		context.Background(),
		domain.ACMEChallengeHTTP01,
		nil,
		nil,
		[]string{"gordon.example.com"},
		nil,
	)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	assert.Equal(t, "http01-gordon.example.com", targets[0].ID)
	assert.Equal(t, []string{"gordon.example.com"}, targets[0].Names)
}

func TestDeriveTargets_IncludesExternalRoutes(t *testing.T) {
	hosts := []out.AppHost{{Host: "app.example.com"}}
	external := map[string]string{"external.example.com": "127.0.0.1:8080"}
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeHTTP01, hosts, external, nil, nil)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	assert.Equal(t, "http01-app.example.com", targets[0].ID)
	assert.Equal(t, "http01-external.example.com", targets[1].ID)
	assert.Equal(t, []string{"external.example.com"}, targets[1].Names)
}

func TestDeriveTargets_SkipsNeverTLSHosts(t *testing.T) {
	hosts := []out.AppHost{
		{Host: "plain.example.com", TLSMode: "never"},
		{Host: "app.example.com", TLSMode: "auto"},
	}
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeHTTP01, hosts, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, targets, 1, "tls=never hosts stay plain HTTP: no certificate target")
	assert.Equal(t, "http01-app.example.com", targets[0].ID)
	assert.Equal(t, []string{"app.example.com"}, targets[0].Names)
}

func TestDeriveTargets_DNS01NilResolver_ReturnsError(t *testing.T) {
	hosts := []out.AppHost{{Host: "app.example.com"}}
	_, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeCloudflareDNS01, hosts, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolver is nil")
}

func TestDeriveTargets_DNS01MismatchedZone_ReturnsError(t *testing.T) {
	hosts := []out.AppHost{{Host: "app.example.com"}}
	resolver := outmocks.NewMockCloudflareZoneResolver(t)
	resolver.EXPECT().FindZone(mock.Anything, mock.Anything).Return(out.CloudflareZone{Name: "other.test"}, nil).Once()
	_, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeCloudflareDNS01, hosts, nil, nil, resolver)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match host")
}

func TestDeriveTargets_DuplicateCanonicalization(t *testing.T) {
	hosts := []out.AppHost{{Host: "App.Example.Com"}, {Host: "app.example.com"}}
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeHTTP01, hosts, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, targets, 1, "mixed-case duplicates should collapse to one target")
	assert.Equal(t, "http01-app.example.com", targets[0].ID)
	assert.Equal(t, []string{"app.example.com"}, targets[0].Names)
}
