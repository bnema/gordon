package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

// TestReservationsFor_InternalHTTPClaimsNothing proves internal HTTP holds
// no global listener reservation: there is no host to reserve, so a second
// app may use the same internal port without conflict.
func TestReservationsFor_InternalHTTPClaimsNothing(t *testing.T) {
	spec := domain.AppSpec{
		Name: "blog",
		Services: []domain.AppService{{
			Name:  "api",
			Image: "registry.example.com/blog/api:1.0.0",
			HTTP:  []domain.AppHTTPInterface{{Port: 8080, Visibility: domain.AppVisibilityInternal}},
		}},
	}
	assert.Empty(t, domain.ReservationsFor(spec))

	public := domain.AppSpec{
		Name: "blog",
		Services: []domain.AppService{{
			Name:  "web",
			Image: "registry.example.com/blog/web:1.0.0",
			HTTP:  []domain.AppHTTPInterface{{Host: "blog.example.com", Port: 8080, TLS: "auto"}},
		}},
	}
	reservations := domain.ReservationsFor(public)
	require.Len(t, reservations, 1)
	assert.Equal(t, "blog.example.com", reservations[0].Host)
}
