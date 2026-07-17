package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouteTargetSnapshotReadyTarget(t *testing.T) {
	entry, err := NewReadyRouteTargetEntry("APP.EXAMPLE.COM", "app-alias", 8080, "http", RouteTargetProtocolH2C, 2)
	require.NoError(t, err)
	require.True(t, entry.Ready())
	require.Equal(t, "app.example.com", entry.CanonicalDomain)
	require.Equal(t, "app-alias", entry.TargetHost)
	require.Equal(t, 8080, entry.TargetPort)
	require.Equal(t, RouteTargetGeneration(2), entry.Generation)
}

func TestRouteTargetSnapshotDrainingTarget(t *testing.T) {
	entry, err := NewDrainingRouteTargetEntry("app.example.com", "10.0.0.3", 8080, "https", RouteTargetProtocolHTTP1, 3)
	require.NoError(t, err)
	require.True(t, entry.Draining())
	require.Equal(t, RouteTargetUnavailableReasonDraining, entry.UnavailableReason)
}

func TestRouteTargetSnapshotUnavailableTargetWithReason(t *testing.T) {
	entry, err := NewUnavailableRouteTargetEntry("app.example.com", RouteTargetUnavailableReasonHealthCheckFailed, 4)
	require.NoError(t, err)
	require.True(t, entry.Unavailable())
	require.Equal(t, RouteTargetUnavailableReasonHealthCheckFailed, entry.UnavailableReason)
}

func TestRouteTargetSnapshotValidateAcceptsCoherentEntries(t *testing.T) {
	ready := mustReadyRouteTargetEntry(t, "ready.example.com", "app-alias", 8080, 1)
	draining, err := NewDrainingRouteTargetEntry("draining.example.com", "10.0.0.3", 8443, "https", RouteTargetProtocolHTTP1, 2)
	require.NoError(t, err)
	unavailable, err := NewUnavailableRouteTargetEntry("unavailable.example.com", RouteTargetUnavailableReasonHealthCheckFailed, 3)
	require.NoError(t, err)

	snapshot := RouteTargetSnapshot{Generation: 3, Entries: []RouteTargetEntry{ready, draining, unavailable}}
	require.NoError(t, snapshot.Validate())
}

func TestRouteTargetSnapshotValidateRejectsInvalidAggregateStateDeterministically(t *testing.T) {
	valid := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 1)

	tests := []struct {
		name     string
		snapshot RouteTargetSnapshot
	}{
		{
			name:     "zero snapshot generation",
			snapshot: RouteTargetSnapshot{Entries: []RouteTargetEntry{valid}},
		},
		{
			name: "zero entry generation",
			snapshot: RouteTargetSnapshot{Generation: 1, Entries: []RouteTargetEntry{{
				CanonicalDomain: "app.example.com", TargetHost: "app", TargetPort: 8080,
				Scheme: "http", Protocol: RouteTargetProtocolHTTP1, Status: RouteTargetStatusReady,
			}}},
		},
		{
			name: "entry newer than snapshot",
			snapshot: RouteTargetSnapshot{Generation: 1, Entries: []RouteTargetEntry{mustReadyRouteTargetEntry(t,
				"app.example.com", "app", 8080, 2)}},
		},
		{
			name: "duplicate canonical domain",
			snapshot: RouteTargetSnapshot{Generation: 2, Entries: []RouteTargetEntry{
				mustReadyRouteTargetEntry(t, "app.example.com", "app-one", 8080, 1),
				mustReadyRouteTargetEntry(t, "app.example.com", "app-two", 8080, 2),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.snapshot.Validate()
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalidRouteSnapshot)
		})
	}
}

func TestRouteTargetSnapshotEntryCanonicalDomainMustBeCanonical(t *testing.T) {
	entry := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 1)
	entry.CanonicalDomain = "APP.EXAMPLE.COM"

	err := entry.Validate()
	require.ErrorIs(t, err, ErrRouteDomainInvalid)
}

func TestRouteTargetSnapshotInvalidDomainRejected(t *testing.T) {
	_, err := NewReadyRouteTargetEntry("localhost", "app", 8080, "http", RouteTargetProtocolHTTP1, 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRouteDomainInvalid))
}

func TestRouteTargetSnapshotInvalidPortRejected(t *testing.T) {
	_, err := NewReadyRouteTargetEntry("app.example.com", "app", 0, "http", RouteTargetProtocolHTTP1, 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidRoute))
}

func TestRouteTargetSnapshotInvalidGenerationRejected(t *testing.T) {
	_, err := NewReadyRouteTargetEntry("app.example.com", "app", 8080, "http", RouteTargetProtocolHTTP1, 0)
	require.ErrorIs(t, err, ErrInvalidRoute)
}

func TestRouteTargetSnapshotGenerationMonotonicComparison(t *testing.T) {
	require.True(t, RouteTargetGeneration(2).After(RouteTargetGeneration(1)))
	require.False(t, RouteTargetGeneration(2).After(RouteTargetGeneration(2)))
}

func TestRouteTargetSnapshotRejectsForbiddenTargetHosts(t *testing.T) {
	for _, host := range []string{
		"unix:///var/run/docker.sock",
		"unix:/run/podman/podman.sock",
		"/var/run/docker.sock",
		"http://app:8080",
		"https://user:password@app.example.com",
		"user:password@app.example.com",
		"app.example.com/path",
		"app.example.com?token=secret",
		"app.example.com#fragment",
		"app.example.com:8080",
		" app.example.com",
		"app.example.com ",
		"app\n.example.com",
		"app\x00.example.com",
	} {
		t.Run(host, func(t *testing.T) {
			_, err := NewReadyRouteTargetEntry("app.example.com", host, 8080, "http", RouteTargetProtocolHTTP1, 1)
			require.ErrorIs(t, err, ErrInvalidRoute)
		})
	}
}

func TestRouteTargetSnapshotAllowsLocalAliasesAndIPsButSplitValidationRejectsLoopback(t *testing.T) {
	for _, host := range []string{"app-alias", "10.0.0.3", "127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			entry := mustReadyRouteTargetEntry(t, "app.example.com", host, 8080, 1)
			require.NoError(t, entry.Validate())
		})
	}

	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		t.Run("split rejects "+host, func(t *testing.T) {
			entry := mustReadyRouteTargetEntry(t, "app.example.com", host, 8080, 1)
			require.ErrorIs(t, entry.ValidateSplitReachability(), ErrRouteTargetNotReachable)
		})
	}

	nonLoopback := mustReadyRouteTargetEntry(t, "app.example.com", "10.0.0.3", 8080, 1)
	require.NoError(t, nonLoopback.ValidateSplitReachability())

	loopback := mustReadyRouteTargetEntry(t, "loopback.example.com", "127.0.0.1", 8080, 1)
	snapshot := RouteTargetSnapshot{Generation: 1, Entries: []RouteTargetEntry{loopback}}
	require.ErrorIs(t, snapshot.ValidateSplitReachability(), ErrRouteTargetNotReachable)
}

func TestRouteTargetSnapshotToProxyTargetPreservesRoutingFieldsOnly(t *testing.T) {
	entry, err := NewReadyRouteTargetEntry("app.example.com", "app", 8080, "http", RouteTargetProtocolH2C, 1)
	require.NoError(t, err)

	target, err := entry.ToProxyTarget()
	require.NoError(t, err)
	require.Equal(t, "app", target.Host)
	require.Equal(t, 8080, target.Port)
	require.Equal(t, "http", target.Scheme)
	require.Equal(t, "h2c", target.Protocol)
	require.Equal(t, "app.example.com", target.RouteHost)
	require.Empty(t, target.ContainerID)
	require.Empty(t, target.OriginalHost)
}

func TestRouteTargetSnapshotToProxyTargetRejectsUnavailableTarget(t *testing.T) {
	entry, err := NewUnavailableRouteTargetEntry("app.example.com", RouteTargetUnavailableReasonNoTarget, 1)
	require.NoError(t, err)

	_, err = entry.ToProxyTarget()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidRoute))
}

func TestRouteTargetSnapshotProtocolFromProxyTarget(t *testing.T) {
	require.Equal(t, RouteTargetProtocolH2C, ProtocolFromProxyTarget(ProxyTarget{Protocol: "h2c"}))
	require.Equal(t, RouteTargetProtocolHTTP1, ProtocolFromProxyTarget(ProxyTarget{}))
}

func TestRouteTargetSnapshotRejectsUnknownUnavailableReason(t *testing.T) {
	_, err := NewUnavailableRouteTargetEntry("app.example.com", RouteTargetUnavailableReason("bad_reason"), 1)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidRoute))
}

func TestRouteTargetSnapshotRejectsIncoherentStateReasonCombinations(t *testing.T) {
	ready := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 1)
	ready.UnavailableReason = RouteTargetUnavailableReasonStarting
	require.Error(t, ready.Validate())

	draining, err := NewDrainingRouteTargetEntry("app.example.com", "app", 8080, "http", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	draining.UnavailableReason = RouteTargetUnavailableReasonNoTarget
	require.Error(t, draining.Validate())

	unavailable, err := NewUnavailableRouteTargetEntry("app.example.com", RouteTargetUnavailableReasonNoTarget, 1)
	require.NoError(t, err)
	unavailable.TargetHost = "app"
	require.Error(t, unavailable.Validate())
}

func TestRouteTargetSnapshotFieldAllowlistPreventsForbiddenData(t *testing.T) {
	// Update these explicit lists only when a documented routing or drain field is added.
	assertExportedFieldNames(t, reflect.TypeFor[RouteTargetSnapshot](), []string{"Generation", "Entries"})
	assertExportedFieldNames(t, reflect.TypeFor[RouteTargetEntry](), []string{
		"CanonicalDomain", "TargetHost", "TargetPort", "Scheme", "Protocol", "Status", "UnavailableReason", "Generation",
	})
	assertJSONFieldNames(t, RouteTargetSnapshot{}, []string{"Entries", "Generation"})
	assertJSONFieldNames(t, RouteTargetEntry{}, []string{
		"CanonicalDomain", "Generation", "Protocol", "Scheme", "Status", "TargetHost", "TargetPort", "UnavailableReason",
	})
}

func mustReadyRouteTargetEntry(t *testing.T, domainName, host string, port int, generation RouteTargetGeneration) RouteTargetEntry {
	t.Helper()
	entry, err := NewReadyRouteTargetEntry(domainName, host, port, "http", RouteTargetProtocolHTTP1, generation)
	require.NoError(t, err)
	return entry
}

func assertExportedFieldNames(t *testing.T, typ reflect.Type, want []string) {
	t.Helper()
	var got []string
	for field := range typ.Fields() {
		if field.IsExported() {
			got = append(got, field.Name)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	require.Equal(t, want, got)
}

func assertJSONFieldNames(t *testing.T, value any, want []string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	got := make([]string, 0, len(fields))
	for field := range fields {
		got = append(got, field)
	}
	sort.Strings(got)
	require.Equal(t, want, got)
}
