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

func newZoneResolverMock(t *testing.T, zone string) *outmocks.MockCloudflareZoneResolver {
	t.Helper()
	resolver := outmocks.NewMockCloudflareZoneResolver(t)
	resolver.EXPECT().FindZone(mock.Anything, mock.Anything).Return(out.CloudflareZone{Name: zone}, nil).Maybe()
	return resolver
}

func TestDeriveTargets_HTTP01PerRoute(t *testing.T) {
	routes := []domain.Route{{Domain: "app.example.com"}, {Domain: "registry.example.com"}}
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeHTTP01, routes, nil, nil)
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
	routes := []domain.Route{{Domain: "app.example.com"}, {Domain: "api.prod.example.com"}, {Domain: "example.com"}}
	resolver := newZoneResolverMock(t, "example.com")
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeCloudflareDNS01, routes, nil, resolver)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	assert.Equal(t, "dns01-example.com", targets[0].ID)
	assert.Equal(t, []string{"example.com", "*.example.com"}, targets[0].Names)
	assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, targets[0].Challenge)
	assert.Equal(t, "dns01-prod.example.com", targets[1].ID)
	assert.Equal(t, []string{"prod.example.com", "*.prod.example.com"}, targets[1].Names)
	assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, targets[1].Challenge)
}

func TestDeriveTargets_IncludesExternalRoutes(t *testing.T) {
	routes := []domain.Route{{Domain: "app.example.com"}}
	external := map[string]string{"external.example.com": "127.0.0.1:8080"}
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeHTTP01, routes, external, nil)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	assert.Equal(t, "http01-app.example.com", targets[0].ID)
	assert.Equal(t, "http01-external.example.com", targets[1].ID)
	assert.Equal(t, []string{"external.example.com"}, targets[1].Names)
}

func TestDeriveTargets_DNS01NilResolver_ReturnsError(t *testing.T) {
	routes := []domain.Route{{Domain: "app.example.com"}}
	_, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeCloudflareDNS01, routes, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolver is nil")
}

func TestDeriveTargets_DNS01MismatchedZone_ReturnsError(t *testing.T) {
	routes := []domain.Route{{Domain: "app.example.com"}}
	resolver := newZoneResolverMock(t, "other.test")
	_, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeCloudflareDNS01, routes, nil, resolver)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match host")
}

func TestDeriveTargets_DuplicateCanonicalization(t *testing.T) {
	routes := []domain.Route{{Domain: "App.Example.Com"}, {Domain: "app.example.com"}}
	targets, err := DeriveCertificateTargets(context.Background(), domain.ACMEChallengeHTTP01, routes, nil, nil)
	require.NoError(t, err)
	require.Len(t, targets, 1, "mixed-case duplicates should collapse to one target")
	assert.Equal(t, "http01-app.example.com", targets[0].ID)
	assert.Equal(t, []string{"app.example.com"}, targets[0].Names)
}
