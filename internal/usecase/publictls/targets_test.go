package publictls

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// fixedZoneResolver returns a fixed zone name for all lookups.
type fixedZoneResolver struct {
	zone string
}

func (f fixedZoneResolver) FindZone(_ context.Context, _ string) (out.CloudflareZone, error) {
	return out.CloudflareZone{Name: f.zone}, nil
}

func TestDeriveTargets_HTTP01PerRoute(t *testing.T) {
	routes := []domain.Route{
		{Domain: "app.example.com"},
		{Domain: "registry.example.com"},
	}
	mode := domain.ACMEChallengeHTTP01

	targets, err := DeriveCertificateTargets(context.Background(), mode, routes, nil, nil)
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
	routes := []domain.Route{
		{Domain: "app.example.com"},
		{Domain: "api.prod.example.com"},
		{Domain: "example.com"},
	}
	mode := domain.ACMEChallengeCloudflareDNS01
	resolver := fixedZoneResolver{zone: "example.com"}

	targets, err := DeriveCertificateTargets(context.Background(), mode, routes, nil, resolver)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	// First target: example.com (includes base and wildcard)
	assert.Equal(t, "dns01-example.com", targets[0].ID)
	assert.Equal(t, []string{"example.com", "*.example.com"}, targets[0].Names)
	assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, targets[0].Challenge)

	// Second target: prod.example.com (includes base and wildcard)
	assert.Equal(t, "dns01-prod.example.com", targets[1].ID)
	assert.Equal(t, []string{"prod.example.com", "*.prod.example.com"}, targets[1].Names)
	assert.Equal(t, domain.ACMEChallengeCloudflareDNS01, targets[1].Challenge)
}

func TestDeriveTargets_IncludesExternalRoutes(t *testing.T) {
	routes := []domain.Route{
		{Domain: "app.example.com"},
	}
	external := map[string]string{
		"external.example.com": "127.0.0.1:8080",
	}
	mode := domain.ACMEChallengeHTTP01

	targets, err := DeriveCertificateTargets(context.Background(), mode, routes, external, nil)
	require.NoError(t, err)
	require.Len(t, targets, 2)

	assert.Equal(t, "http01-app.example.com", targets[0].ID)
	assert.Equal(t, "http01-external.example.com", targets[1].ID)
	assert.Equal(t, []string{"external.example.com"}, targets[1].Names)
}

func TestDeriveTargets_DNS01NilResolver_ReturnsError(t *testing.T) {
	routes := []domain.Route{
		{Domain: "app.example.com"},
	}
	mode := domain.ACMEChallengeCloudflareDNS01

	_, err := DeriveCertificateTargets(context.Background(), mode, routes, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolver is nil")
}

func TestDeriveTargets_DuplicateCanonicalization(t *testing.T) {
	routes := []domain.Route{
		{Domain: "App.Example.Com"},
		{Domain: "app.example.com"},
	}
	mode := domain.ACMEChallengeHTTP01

	targets, err := DeriveCertificateTargets(context.Background(), mode, routes, nil, nil)
	require.NoError(t, err)
	require.Len(t, targets, 1, "mixed-case duplicates should collapse to one target")

	assert.Equal(t, "http01-app.example.com", targets[0].ID)
	assert.Equal(t, []string{"app.example.com"}, targets[0].Names)
}
