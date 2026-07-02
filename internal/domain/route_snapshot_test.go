package domain

import (
	"errors"
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

func TestRouteTargetSnapshotGenerationMonotonicComparison(t *testing.T) {
	require.True(t, RouteTargetGeneration(2).After(RouteTargetGeneration(1)))
	require.False(t, RouteTargetGeneration(2).After(RouteTargetGeneration(2)))
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
	ready, err := NewReadyRouteTargetEntry("app.example.com", "app", 8080, "http", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
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
