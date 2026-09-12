package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func reservationSpec() domain.AppSpec {
	return domain.AppSpec{
		Name: "blog",
		Services: []domain.AppService{
			{
				Name: "web", Image: "img:1",
				HTTP: []domain.AppHTTPInterface{{Host: "b.example.com", Port: 8080, TLS: "auto"}},
				TCP:  []domain.AppTCPInterface{{Entrypoint: "tcp", Port: 9000, Publish: "9000"}},
			},
		},
	}
}

func TestReservationsFor_SortedAndNamespaced(t *testing.T) {
	reservations := domain.ReservationsFor(reservationSpec())
	require.Len(t, reservations, 2)
	assert.Equal(t, "http", reservations[0].Proto)
	assert.Equal(t, "tcp", reservations[1].Proto)
	assert.Equal(t, "dual", reservations[1].IP)
	assert.Equal(t, "web", reservations[0].Service)
	assert.Equal(t, "blog", reservations[0].App)
}

func TestCheckReservations_RejectsDuplicateInsideCandidate(t *testing.T) {
	spec := reservationSpec()
	dup := spec.Services[0]
	dup.Name = "web2"
	spec.Services = append(spec.Services, dup)

	err := domain.CheckReservations(nil, domain.ReservationsFor(spec), "blog")
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)
	assert.Contains(t, err.Error(), "claims")
}

func TestCheckReservations_RejectsWildcardOverlapInsideCandidate(t *testing.T) {
	candidate := []domain.AppListenerReservation{
		{Proto: "tcp", IP: "0.0.0.0", Port: 9000, Service: "one", App: "tcp-overlap"},
		{Proto: "tcp", IP: "127.0.0.1", Port: 9000, Service: "two", App: "tcp-overlap"},
	}
	err := domain.CheckReservations(nil, candidate, "tcp-overlap")
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)
}

func TestCheckReservations_RejectsCrossAppWildcardConflict(t *testing.T) {
	existing := []domain.AppListenerReservation{
		{Proto: "tcp", IP: "dual", Port: 9000, Service: "s", App: "other"},
	}
	candidate := []domain.AppListenerReservation{
		{Proto: "tcp", IP: "127.0.0.1", Port: 9000, Service: "s", App: "shop"},
	}
	err := domain.CheckReservations(existing, candidate, "shop")
	require.ErrorIs(t, err, domain.ErrAppReservationConflict)
	assert.Contains(t, err.Error(), "conflicts with")
}

func TestCheckReservations_SameAppNeverSelfConflicts(t *testing.T) {
	existing := []domain.AppListenerReservation{
		{Proto: "http", Host: "blog.example.com", Service: "web", App: "blog"},
	}
	candidate := []domain.AppListenerReservation{
		{Proto: "http", Host: "blog.example.com", Service: "web", App: "blog"},
	}
	require.NoError(t, domain.CheckReservations(existing, candidate, "blog"))
}

func TestCheckReservations_SamePortDifferentProtoCoexists(t *testing.T) {
	candidate := []domain.AppListenerReservation{
		{Proto: "tcp", IP: "0.0.0.0", Port: 9000, Service: "s", App: "a"},
		{Proto: "udp", IP: "0.0.0.0", Port: 9000, Service: "s", App: "a"},
	}
	require.NoError(t, domain.CheckReservations(nil, candidate, "a"))
}
