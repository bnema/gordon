package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func visibilitySpec(interfaces ...domain.AppHTTPInterface) domain.AppSpec {
	return domain.AppSpec{
		Name: "blog",
		Services: []domain.AppService{{
			Name:      "web",
			Image:     "registry.example.com/blog/web:1.4.2",
			StopGrace: 10 * time.Second,
			Readiness: domain.AppReadiness{Type: domain.AppReadinessHTTP, Path: "/healthz", Timeout: 30 * time.Second},
			HTTP:      interfaces,
		}},
	}
}

// TestAppHTTPInterface_EffectiveVisibility proves the zero value normalizes
// to public so manifests and stored state written before visibility existed
// keep their meaning.
func TestAppHTTPInterface_EffectiveVisibility(t *testing.T) {
	absent := domain.AppHTTPInterface{Host: "blog.example.com", Port: 8080, TLS: "auto"}
	assert.Equal(t, domain.AppVisibilityPublic, absent.EffectiveVisibility())
	assert.True(t, absent.IsPublic())
	assert.False(t, absent.IsInternal())

	explicit := absent
	explicit.Visibility = domain.AppVisibilityInternal
	assert.Equal(t, domain.AppVisibilityInternal, explicit.EffectiveVisibility())
	assert.True(t, explicit.IsInternal())
	assert.False(t, explicit.IsPublic())
}

// TestAppSpec_ValidateVisibility proves public and absent visibility stay
// valid, internal visibility accepts a port without host or tls, and every
// malformed combination is rejected.
func TestAppSpec_ValidateVisibility(t *testing.T) {
	t.Run("absent visibility remains a valid public manifest", func(t *testing.T) {
		require.NoError(t, visibilitySpec(domain.AppHTTPInterface{
			Host: "blog.example.com", Port: 8080, TLS: "auto",
		}).Validate())
	})

	t.Run("explicit public remains valid", func(t *testing.T) {
		require.NoError(t, visibilitySpec(domain.AppHTTPInterface{
			Host: "blog.example.com", Port: 8080, TLS: "auto", Visibility: domain.AppVisibilityPublic,
		}).Validate())
	})

	t.Run("internal accepts a port without host or tls", func(t *testing.T) {
		require.NoError(t, visibilitySpec(domain.AppHTTPInterface{
			Port: 8080, Visibility: domain.AppVisibilityInternal,
		}).Validate())
	})

	reject := map[string]domain.AppHTTPInterface{
		"internal forbids host":           {Host: "blog.example.com", Port: 8080, Visibility: domain.AppVisibilityInternal},
		"internal forbids tls":            {Port: 8080, TLS: "auto", Visibility: domain.AppVisibilityInternal},
		"unknown visibility":              {Host: "blog.example.com", Port: 8080, TLS: "auto", Visibility: "vpn"},
		"internal without port":           {Visibility: domain.AppVisibilityInternal},
		"public without host":             {Port: 8080, TLS: "auto"},
		"public without valid host":       {Host: "not a host", Port: 8080, TLS: "auto"},
		"public without tls mode":         {Host: "blog.example.com", Port: 8080},
		"public with unknown tls mode":    {Host: "blog.example.com", Port: 8080, TLS: "sometimes"},
		"public with out of range port":   {Host: "blog.example.com", Port: 0, TLS: "auto"},
		"internal with out of range port": {Port: -1, Visibility: domain.AppVisibilityInternal},
	}
	for name, iface := range reject {
		t.Run(name, func(t *testing.T) {
			err := visibilitySpec(iface).Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidAppSpec)
		})
	}
}

// TestAppService_InternallyOnlyPort proves a port is internal-only only
// when internal HTTP declares it and no externally backed interface does.
func TestAppService_InternallyOnlyPort(t *testing.T) {
	internalOnly := domain.AppService{
		HTTP: []domain.AppHTTPInterface{{Port: 8080, Visibility: domain.AppVisibilityInternal}},
	}
	assert.True(t, internalOnly.InternallyOnlyPort(8080))
	assert.False(t, internalOnly.InternallyOnlyPort(8081))
	assert.True(t, internalOnly.IsPublicHTTP() == false)

	mixed := domain.AppService{
		HTTP: []domain.AppHTTPInterface{
			{Host: "blog.example.com", Port: 8080, TLS: "auto"},
			{Port: 9090, Visibility: domain.AppVisibilityInternal},
		},
	}
	assert.True(t, mixed.IsPublicHTTP())
	assert.True(t, mixed.InternallyOnlyPort(9090))
	assert.False(t, mixed.InternallyOnlyPort(8080))

	// A TCP interface on the same port is externally backed: the port is
	// never internal-only, even though an internal HTTP interface wants it.
	// Validation rejects that combination, and the read helper must not
	// treat it as internal-only either.
	shadowed := domain.AppService{
		HTTP: []domain.AppHTTPInterface{{Port: 8080, Visibility: domain.AppVisibilityInternal}},
		TCP:  []domain.AppTCPInterface{{Port: 8080}},
	}
	assert.False(t, shadowed.InternallyOnlyPort(8080))
}

// TestAppSpec_ValidateInterfacePorts proves an internal HTTP port may not
// collide with an externally backed interface on the same container port,
// and that internal ports stay unique.
func TestAppSpec_ValidateInterfacePorts(t *testing.T) {
	collide := visibilitySpec(
		domain.AppHTTPInterface{Port: 8080, Visibility: domain.AppVisibilityInternal},
	)
	collide.Services[0].TCP = []domain.AppTCPInterface{{Port: 8080}}
	require.ErrorIs(t, collide.Validate(), domain.ErrInvalidAppSpec)

	duplicate := visibilitySpec(
		domain.AppHTTPInterface{Port: 8080, Visibility: domain.AppVisibilityInternal},
		domain.AppHTTPInterface{Port: 8080, Visibility: domain.AppVisibilityInternal},
	)
	require.ErrorIs(t, duplicate.Validate(), domain.ErrInvalidAppSpec)
}
