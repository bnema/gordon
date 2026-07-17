package domain

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouteTargetSnapshotReadyTargetIsAttachedAndOpaque(t *testing.T) {
	entry := mustReadyRouteTargetEntry(t, "APP.EXAMPLE.COM", "app_alias", 8080, 2)

	require.True(t, entry.Ready())
	require.Equal(t, "app.example.com", entry.CanonicalDomain)
	require.Equal(t, RouteTargetAttachmentAttached, entry.Attachment)
	require.Empty(t, entry.UpstreamHost)
	require.True(t, entry.TargetKey.Valid())
	require.NotContains(t, string(entry.TargetKey), entry.TargetHost)
}

func TestRouteTargetSnapshotExternalTargetPreservesHostHeaderWithoutLeakage(t *testing.T) {
	entry, err := NewExternalReadyRouteTargetEntry("app.example.com", "198.51.100.9", "Api.Upstream.Example.", 8443, "https", RouteTargetProtocolHTTP1, 7)
	require.NoError(t, err)
	require.Equal(t, RouteTargetAttachmentNotRequired, entry.Attachment)
	require.Equal(t, "Api.Upstream.Example.", entry.UpstreamHost)

	target, err := entry.ToProxyTarget()
	require.NoError(t, err)
	require.Equal(t, "198.51.100.9", target.Host)
	require.Equal(t, "Api.Upstream.Example.", target.OriginalHost)
	require.Empty(t, target.ContainerID)

	encoded, err := json.Marshal(entry)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "http://")
	require.NotContains(t, string(encoded), "password")
}

func TestRouteTargetSnapshotManagedTargetHasNoOriginalHost(t *testing.T) {
	entry := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 1)
	target, err := entry.ToProxyTarget()
	require.NoError(t, err)
	require.Empty(t, target.OriginalHost)
}

func TestRouteTargetSnapshotEndpointHostValidationAcceptsAliasesDNSAndIPs(t *testing.T) {
	for _, host := range []string{
		"app_alias", "app_alias.service", "service.example.com", "service.example.com.", "10.0.0.3", "2001:db8::1", "::1",
	} {
		t.Run(host, func(t *testing.T) {
			entry := mustReadyRouteTargetEntry(t, "app.example.com", host, 8080, 1)
			require.NoError(t, entry.Validate())
		})
	}
}

func TestRouteTargetSnapshotEndpointHostValidationRejectsUnsafeEndpoints(t *testing.T) {
	overlongLabel := strings.Repeat("a", 64) + ".example.com"
	overlongHost := strings.Repeat("a", 254)
	for _, host := range []string{
		"", " app.example.com", "app.example.com ", "app\n.example.com", "app\x00.example.com",
		"unix:///var/run/docker.sock", "unix:/run/podman/podman.sock", "/var/run/docker.sock", "app.sock",
		"http://app:8080", "https://user:password@app.example.com", "user:password@app.example.com",
		"app.example.com/path", "app.example.com?token=secret", "app.example.com#fragment", "app.example.com:8080",
		".app.example.com", "app..example.com", "app.example.com..", overlongLabel, overlongHost,
	} {
		t.Run(host, func(t *testing.T) {
			_, err := NewReadyRouteTargetEntry("app.example.com", host, 8080, "http", RouteTargetProtocolHTTP1, 1)
			require.ErrorIs(t, err, ErrInvalidRoute)
		})
	}
}

func TestRouteTargetSnapshotExternalTargetRejectsUnsafeUpstreamHost(t *testing.T) {
	for _, upstreamHost := range []string{"https://user:secret@upstream.example", "upstream.example:443", "upstream.example/path", "upstream..example"} {
		t.Run(upstreamHost, func(t *testing.T) {
			_, err := NewExternalReadyRouteTargetEntry("app.example.com", "198.51.100.9", upstreamHost, 443, "https", RouteTargetProtocolHTTP1, 1)
			require.ErrorIs(t, err, ErrInvalidRoute)
		})
	}
}

func TestRouteTargetSnapshotValidateRequiresCoherentAttachmentState(t *testing.T) {
	attached := mustReadyRouteTargetEntry(t, "app.example.com", "app_alias", 8080, 1)
	attached.Attachment = RouteTargetAttachmentNotRequired
	require.ErrorIs(t, attached.Validate(), ErrInvalidRoute)

	external, err := NewExternalReadyRouteTargetEntry("external.example.com", "198.51.100.9", "upstream.example", 443, "https", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	external.Attachment = RouteTargetAttachmentAttached
	require.ErrorIs(t, external.Validate(), ErrInvalidRoute)

	unavailable, err := NewUnavailableRouteTargetEntry("offline.example.com", RouteTargetUnavailableReasonStarting, 1)
	require.NoError(t, err)
	unavailable.Attachment = RouteTargetAttachmentAttached
	require.ErrorIs(t, unavailable.Validate(), ErrInvalidRoute)
}

func TestRouteTargetSnapshotAggregateValidatesRegistryDomainGenerationAndTargetKeyUniqueness(t *testing.T) {
	app := mustReadyRouteTargetEntry(t, "app.example.com", "app_alias", 8080, 1)
	registry := mustReadyRouteTargetEntry(t, "registry.example.com", "registry_alias", 5000, 2)
	snapshot := RouteTargetSnapshot{Generation: 2, Entries: []RouteTargetEntry{app}, RegistryForwardingTarget: &registry}
	require.NoError(t, snapshot.Validate())

	duplicateDomain := registry
	duplicateDomain.CanonicalDomain = app.CanonicalDomain
	require.ErrorIs(t, (RouteTargetSnapshot{Generation: 2, Entries: []RouteTargetEntry{app}, RegistryForwardingTarget: &duplicateDomain}).Validate(), ErrInvalidRouteSnapshot)

	duplicateKey := registry
	duplicateKey.TargetKey = app.TargetKey
	require.ErrorIs(t, (RouteTargetSnapshot{Generation: 2, Entries: []RouteTargetEntry{app}, RegistryForwardingTarget: &duplicateKey}).Validate(), ErrInvalidRouteSnapshot)

	future := registry
	future.Generation = 3
	require.ErrorIs(t, (RouteTargetSnapshot{Generation: 2, RegistryForwardingTarget: &future}).Validate(), ErrInvalidRouteSnapshot)
}

func TestRouteTargetSnapshotSplitReachabilityChecksEntriesAndRegistry(t *testing.T) {
	app := mustReadyRouteTargetEntry(t, "app.example.com", "app_alias", 8080, 1)
	registry := mustReadyRouteTargetEntry(t, "registry.example.com", "127.0.0.1", 5000, 1)
	snapshot := RouteTargetSnapshot{Generation: 1, Entries: []RouteTargetEntry{app}, RegistryForwardingTarget: &registry}
	require.ErrorIs(t, snapshot.ValidateSplitReachability(), ErrRouteTargetNotReachable)

	registry = mustReadyRouteTargetEntry(t, "registry.example.com", "registry_alias", 5000, 1)
	snapshot.RegistryForwardingTarget = &registry
	require.NoError(t, snapshot.ValidateSplitReachability())
}

func TestRouteTargetSnapshotSplitValidationRejectsLoopbackButAllowsExternalNoAttachment(t *testing.T) {
	loopback := mustReadyRouteTargetEntry(t, "loopback.example.com", "127.0.0.1", 8080, 1)
	require.ErrorIs(t, loopback.ValidateSplitReachability(), ErrRouteTargetNotReachable)

	external, err := NewExternalReadyRouteTargetEntry("external.example.com", "198.51.100.9", "upstream.example", 443, "https", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	require.NoError(t, external.ValidateSplitReachability())
}

func TestRouteTargetSnapshotRouteTargetKeyIsOpaqueAndValidated(t *testing.T) {
	valid, err := NewRouteTargetKey("rtk_abcdefghijklmnopqrstuvwxyz234567")
	require.NoError(t, err)
	require.True(t, valid.Valid())

	for _, key := range []string{"", "container-id", "abcdef0123456789", "rtk_short", "rtk_ABCDEFGHIJKLMNOP", "rtk_abcdefghijklmnopqrstuvwxyz234567!"} {
		t.Run(key, func(t *testing.T) {
			_, err := NewRouteTargetKey(key)
			require.ErrorIs(t, err, ErrInvalidRoute)
		})
	}
}

func TestRouteTargetSnapshotRejectsInvalidDomainPortGenerationAndState(t *testing.T) {
	_, err := NewReadyRouteTargetEntry("localhost", "app", 8080, "http", RouteTargetProtocolHTTP1, 1)
	require.ErrorIs(t, err, ErrRouteDomainInvalid)
	_, err = NewReadyRouteTargetEntry("app.example.com", "app", 0, "http", RouteTargetProtocolHTTP1, 1)
	require.ErrorIs(t, err, ErrInvalidRoute)
	_, err = NewReadyRouteTargetEntry("app.example.com", "app", 8080, "http", RouteTargetProtocolHTTP1, 0)
	require.ErrorIs(t, err, ErrInvalidRoute)

	unavailable, err := NewUnavailableRouteTargetEntry("app.example.com", RouteTargetUnavailableReasonNoTarget, 1)
	require.NoError(t, err)
	_, err = unavailable.ToProxyTarget()
	require.ErrorIs(t, err, ErrInvalidRoute)
}

func TestRouteTargetSnapshotFieldAllowlistPreventsForbiddenData(t *testing.T) {
	// Update these explicit lists only when a documented routing or drain field is added.
	assertExportedFieldNames(t, reflect.TypeFor[RouteTargetSnapshot](), []string{"Generation", "Entries", "RegistryForwardingTarget"})
	assertExportedFieldNames(t, reflect.TypeFor[RouteTargetEntry](), []string{
		"Attachment", "CanonicalDomain", "Generation", "Protocol", "Scheme", "Status", "TargetHost", "TargetKey", "TargetPort", "UnavailableReason", "UpstreamHost",
	})
	assertJSONFieldNames(t, RouteTargetSnapshot{}, []string{"Entries", "Generation", "RegistryForwardingTarget"})
	assertJSONFieldNames(t, RouteTargetEntry{}, []string{
		"Attachment", "CanonicalDomain", "Generation", "Protocol", "Scheme", "Status", "TargetHost", "TargetKey", "TargetPort", "UnavailableReason", "UpstreamHost",
	})
}

func TestRouteTargetSnapshotProtocolFromProxyTarget(t *testing.T) {
	require.Equal(t, RouteTargetProtocolH2C, ProtocolFromProxyTarget(ProxyTarget{Protocol: "h2c"}))
	require.Equal(t, RouteTargetProtocolHTTP1, ProtocolFromProxyTarget(ProxyTarget{}))
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
	sort.Strings(want)
	require.Equal(t, want, got)
}
