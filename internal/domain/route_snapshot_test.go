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

func TestRouteTargetSnapshotHasNoContainerIdentityField(t *testing.T) {
	entry := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 1)
	snapshot := RouteTargetSnapshot{Generation: 1, Entries: []RouteTargetEntry{entry}}
	require.NoError(t, snapshot.Validate())
	encoded, err := json.Marshal(snapshot)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "container-private-id")

	entryType := reflect.TypeFor[RouteTargetEntry]()
	for field := range entryType.Fields() {
		require.NotEqual(t, "ContainerID", field.Name)
		require.NotEqual(t, "ContainerId", field.Name)
	}
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
	require.Empty(t, target.RouteHost, "external targets must preserve their upstream Host header")
	require.Equal(t, entry.TargetKey, target.TargetKey)
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
		"app_alias", "app_alias.service", "app.sock", "docker.sock", "service.example.com", "service.example.com.", "10.0.0.3", "2001:db8::1", "::1",
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
		"unix:///var/run/docker.sock", "unix:/run/podman/podman.sock", "/var/run/docker.sock",
		"http://app:8080", "https://user:password@app.example.com", "user:password@app.example.com",
		"app.example.com/path", "app.example.com?token=secret", "app.example.com#fragment", "app.example.com:8080",
		".app.example.com", "app..example.com", "app.example.com..", "2001:db8::1.", overlongLabel, overlongHost,
	} {
		t.Run(host, func(t *testing.T) {
			_, err := NewReadyRouteTargetEntry("app.example.com", host, 8080, "http", RouteTargetProtocolHTTP1, 1)
			require.ErrorIs(t, err, ErrInvalidRoute)
		})
	}
}

func TestRouteTargetSnapshotEndpointHostValidationAcceptsMaximalAbsoluteFQDN(t *testing.T) {
	maxHostname := strings.Join([]string{
		strings.Repeat("a", 63), strings.Repeat("b", 63), strings.Repeat("c", 63), strings.Repeat("d", 61),
	}, ".")
	require.Len(t, maxHostname, 253)

	entry, err := NewReadyRouteTargetEntry("app.example.com", maxHostname+".", 8080, "http", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	require.NoError(t, entry.Validate())

	_, err = NewReadyRouteTargetEntry("app.example.com", maxHostname+"a", 8080, "http", RouteTargetProtocolHTTP1, 1)
	require.ErrorIs(t, err, ErrInvalidRoute)
}

func TestRouteTargetSnapshotExternalTargetRejectsUnsafeUpstreamHost(t *testing.T) {
	for _, upstreamHost := range []string{"https://user:secret@upstream.example", "upstream.example:443", "upstream.example/path", "upstream..example", "2001:db8::1."} {
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

func TestNewDrainingRouteTargetEntrySetsDrainingStateAndReason(t *testing.T) {
	tests := []struct {
		name       string
		scheme     string
		protocol   RouteTargetProtocol
		generation RouteTargetGeneration
	}{
		{name: "http1", scheme: "http", protocol: RouteTargetProtocolHTTP1, generation: 1},
		{name: "h2c", scheme: "https", protocol: RouteTargetProtocolH2C, generation: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := NewDrainingRouteTargetEntry("app.example.com", "app", 8080, tt.scheme, tt.protocol, tt.generation)
			require.NoError(t, err)
			require.Equal(t, RouteTargetStatusDraining, entry.Status)
			require.Equal(t, RouteTargetUnavailableReasonDraining, entry.UnavailableReason)
			require.True(t, entry.Draining())
		})
	}
}

func TestNewUnavailableRouteTargetEntryAcceptsEverySupportedReasonAndRejectsUnknown(t *testing.T) {
	tests := []struct {
		name   string
		reason RouteTargetUnavailableReason
		valid  bool
	}{
		{name: "no target", reason: RouteTargetUnavailableReasonNoTarget, valid: true},
		{name: "starting", reason: RouteTargetUnavailableReasonStarting, valid: true},
		{name: "health check failed", reason: RouteTargetUnavailableReasonHealthCheckFailed, valid: true},
		{name: "deployment", reason: RouteTargetUnavailableReasonDeployment, valid: true},
		{name: "draining belongs to draining state", reason: RouteTargetUnavailableReasonDraining},
		{name: "unknown", reason: RouteTargetUnavailableReason("unknown")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := NewUnavailableRouteTargetEntry("app.example.com", tt.reason, 1)
			if !tt.valid {
				require.ErrorIs(t, err, ErrInvalidRoute)
				return
			}
			require.NoError(t, err)
			require.Equal(t, RouteTargetStatusUnavailable, entry.Status)
			require.Equal(t, tt.reason, entry.UnavailableReason)
			require.True(t, entry.Unavailable())
		})
	}
}

func TestRouteTargetGenerationAfter(t *testing.T) {
	tests := []struct {
		name     string
		current  RouteTargetGeneration
		previous RouteTargetGeneration
		want     bool
	}{
		{name: "newer", current: 2, previous: 1, want: true},
		{name: "equal", current: 2, previous: 2, want: false},
		{name: "older", current: 1, previous: 2, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.current.After(tt.previous))
		})
	}
}

func TestRouteTargetSnapshotProtocolFromProxyTarget(t *testing.T) {
	tests := []struct {
		name   string
		target ProxyTarget
		want   RouteTargetProtocol
	}{
		{name: "h2c", target: ProxyTarget{Protocol: "h2c"}, want: RouteTargetProtocolH2C},
		{name: "http1 default", target: ProxyTarget{}, want: RouteTargetProtocolHTTP1},
		{name: "unknown defaults to http1", target: ProxyTarget{Protocol: "http2"}, want: RouteTargetProtocolHTTP1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, ProtocolFromProxyTarget(tt.target))
		})
	}
}

func TestRouteTargetEntryToProxyTargetConvertsManagedAndExternalTargets(t *testing.T) {
	managed, err := NewReadyRouteTargetEntry("app.example.com", "app", 8080, "http", RouteTargetProtocolH2C, 1)
	require.NoError(t, err)
	external, err := NewExternalReadyRouteTargetEntry("external.example.com", "198.51.100.9", "upstream.example", 8443, "https", RouteTargetProtocolHTTP1, 2)
	require.NoError(t, err)

	tests := []struct {
		name  string
		entry RouteTargetEntry
		want  ProxyTarget
	}{
		{
			name:  "managed h2c",
			entry: managed,
			want:  ProxyTarget{Host: "app", Port: 8080, TargetKey: managed.TargetKey, Scheme: "http", Protocol: "h2c", RouteHost: "app.example.com"},
		},
		{
			name:  "external original host",
			entry: external,
			want:  ProxyTarget{Host: "198.51.100.9", Port: 8443, TargetKey: external.TargetKey, Scheme: "https", OriginalHost: "upstream.example"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := tt.entry.ToProxyTarget()
			require.NoError(t, err)
			require.Equal(t, tt.want, target)
			require.Empty(t, target.ContainerID)
		})
	}
}

func TestRouteTargetSnapshotTargetKeyIsStableAcrossGenerationsAndStatus(t *testing.T) {
	first := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 1)
	second := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 2)
	draining, err := NewDrainingRouteTargetEntry("app.example.com", "app", 8080, "http", RouteTargetProtocolHTTP1, 3)
	require.NoError(t, err)

	require.Equal(t, first.TargetKey, second.TargetKey)
	require.Equal(t, first.TargetKey, draining.TargetKey)
}

func TestRouteTargetSnapshotTargetKeyChangesForRoutingTargetChanges(t *testing.T) {
	base, err := NewExternalReadyRouteTargetEntry("app.example.com", "198.51.100.9", "upstream.example", 8443, "https", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func(*RouteTargetEntry)
	}{
		{name: "canonical route", mutate: func(entry *RouteTargetEntry) { entry.CanonicalDomain = "other.example.com" }},
		{name: "target host", mutate: func(entry *RouteTargetEntry) { entry.TargetHost = "198.51.100.10" }},
		{name: "target port", mutate: func(entry *RouteTargetEntry) { entry.TargetPort = 9443 }},
		{name: "scheme", mutate: func(entry *RouteTargetEntry) { entry.Scheme = "http" }},
		{name: "protocol", mutate: func(entry *RouteTargetEntry) { entry.Protocol = RouteTargetProtocolH2C }},
		{name: "upstream host", mutate: func(entry *RouteTargetEntry) { entry.UpstreamHost = "other-upstream.example" }},
		{name: "attachment", mutate: func(entry *RouteTargetEntry) { entry.Attachment = RouteTargetAttachmentAttached }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base
			tt.mutate(&changed)
			changed.setDerivedTargetKey()
			require.NotEqual(t, base.TargetKey, changed.TargetKey)
		})
	}
}

func TestRouteTargetEntryValidateRejectsStaleOrForgedTargetKeys(t *testing.T) {
	base, err := NewExternalReadyRouteTargetEntry("app.example.com", "198.51.100.9", "upstream.example", 8443, "https", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func(*RouteTargetEntry)
	}{
		{name: "canonical domain", mutate: func(entry *RouteTargetEntry) { entry.CanonicalDomain = "other.example.com" }},
		{name: "target host", mutate: func(entry *RouteTargetEntry) { entry.TargetHost = "198.51.100.10" }},
		{name: "target port", mutate: func(entry *RouteTargetEntry) { entry.TargetPort = 9443 }},
		{name: "scheme", mutate: func(entry *RouteTargetEntry) { entry.Scheme = "http" }},
		{name: "protocol", mutate: func(entry *RouteTargetEntry) { entry.Protocol = RouteTargetProtocolH2C }},
		{name: "upstream host", mutate: func(entry *RouteTargetEntry) { entry.UpstreamHost = "other-upstream.example" }},
		{name: "attachment", mutate: func(entry *RouteTargetEntry) { entry.Attachment = RouteTargetAttachmentAttached }},
	}
	for _, tt := range tests {
		t.Run(tt.name+" stale", func(t *testing.T) {
			stale := base
			tt.mutate(&stale)
			require.True(t, stale.TargetKey.Valid())
			require.ErrorIs(t, stale.Validate(), ErrInvalidRoute)
		})
		t.Run(tt.name+" forged", func(t *testing.T) {
			forged := base
			tt.mutate(&forged)
			forged.TargetKey = RouteTargetKey("rtk_abcdefghijklmnopqrstuvwxyz234567")
			require.True(t, forged.TargetKey.Valid())
			require.ErrorIs(t, forged.Validate(), ErrInvalidRoute)
		})
	}
}

func TestRouteTargetEntryTargetKeyCanonicalizesEndpointIdentity(t *testing.T) {
	dnsLower := mustReadyRouteTargetEntry(t, "app.example.com", "app_alias", 8080, 1)
	dnsUpper := mustReadyRouteTargetEntry(t, "app.example.com", "APP_ALIAS.", 8080, 2)
	require.Equal(t, dnsLower.TargetKey, dnsUpper.TargetKey)

	ipCompressed := mustReadyRouteTargetEntry(t, "app.example.com", "2001:db8::1", 8080, 1)
	ipExpanded := mustReadyRouteTargetEntry(t, "app.example.com", "2001:0db8:0:0:0:0:0:1", 8080, 2)
	require.Equal(t, ipCompressed.TargetKey, ipExpanded.TargetKey)

	externalLower, err := NewExternalReadyRouteTargetEntry("external.example.com", "2001:db8::1", "api.upstream.example", 443, "https", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	externalEquivalent, err := NewExternalReadyRouteTargetEntry("external.example.com", "2001:0db8:0:0:0:0:0:1", "API.UPSTREAM.EXAMPLE.", 443, "https", RouteTargetProtocolHTTP1, 2)
	require.NoError(t, err)
	require.Equal(t, externalLower.TargetKey, externalEquivalent.TargetKey)

	changed, err := NewExternalReadyRouteTargetEntry("external.example.com", "2001:db8::2", "api.upstream.example", 443, "https", RouteTargetProtocolHTTP1, 1)
	require.NoError(t, err)
	require.NotEqual(t, externalLower.TargetKey, changed.TargetKey)
}

func TestRouteTargetSnapshotCloneIsIndependent(t *testing.T) {
	entry := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 1)
	registry := mustReadyRouteTargetEntry(t, "registry.example.com", "registry", 5000, 1)
	original := RouteTargetSnapshot{
		Generation:               1,
		Entries:                  []RouteTargetEntry{entry},
		RegistryForwardingTarget: &registry,
	}

	clone := original.Clone()
	require.Equal(t, original, clone)
	require.NotSame(t, &original.Entries[0], &clone.Entries[0])
	require.NotSame(t, original.RegistryForwardingTarget, clone.RegistryForwardingTarget)

	clone.Entries[0].TargetHost = "clone"
	clone.RegistryForwardingTarget.TargetHost = "clone-registry"
	require.Equal(t, "app", original.Entries[0].TargetHost)
	require.Equal(t, "registry", original.RegistryForwardingTarget.TargetHost)

	original.Entries[0].TargetHost = "original"
	original.RegistryForwardingTarget.TargetHost = "original-registry"
	require.Equal(t, "clone", clone.Entries[0].TargetHost)
	require.Equal(t, "clone-registry", clone.RegistryForwardingTarget.TargetHost)
}

func TestRouteTargetSnapshotCloneSupportsConcurrentIndependentUse(t *testing.T) {
	entry := mustReadyRouteTargetEntry(t, "app.example.com", "app", 8080, 1)
	registry := mustReadyRouteTargetEntry(t, "registry.example.com", "registry", 5000, 1)
	snapshot := RouteTargetSnapshot{Generation: 1, Entries: []RouteTargetEntry{entry}, RegistryForwardingTarget: &registry}

	for range 16 {
		clone := snapshot.Clone()
		t.Run("independent clone", func(t *testing.T) {
			t.Parallel()
			clone.Entries[0].TargetHost = "worker"
			clone.RegistryForwardingTarget.TargetHost = "worker-registry"
			require.Equal(t, "worker", clone.Entries[0].TargetHost)
			require.Equal(t, "worker-registry", clone.RegistryForwardingTarget.TargetHost)
		})
	}
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
