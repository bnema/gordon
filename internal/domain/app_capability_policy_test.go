package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validCapabilityPolicy() AppCapabilityPolicy {
	return AppCapabilityPolicy{
		Name:            "net-admin",
		Add:             []string{"NET_ADMIN"},
		AllowedApps:     []string{"demo"},
		AllowedServices: []string{"worker"},
	}
}

func TestCapabilityPolicyValid(t *testing.T) {
	p := validCapabilityPolicy()
	require.NoError(t, p.Validate())
	p.Add = []string{"NET_ADMIN", "IPC_LOCK", "SYS_NICE"}
	require.NoError(t, p.Validate())
}

func TestCapabilityPolicyNameRejected(t *testing.T) {
	for _, name := range []string{"", "Bad", "a--b", "has space"} {
		p := validCapabilityPolicy()
		p.Name = name
		err := p.Validate()
		require.Error(t, err, "name %q", name)
		assert.ErrorIs(t, err, ErrCapabilityPolicy)
	}
}

func TestCapabilityPolicyAddRejected(t *testing.T) {
	cases := map[string][]string{
		"empty":       {},
		"empty entry": {""},
		"ALL":         {"ALL"},
		"SYS_ADMIN":   {"SYS_ADMIN"},
		"SYS_PTRACE":  {"SYS_PTRACE"},
		"SYS_MODULE":  {"SYS_MODULE"},
		"BPF":         {"BPF"},
		"lowercase":   {"net_admin"},
		"CAP_ prefix": {"CAP_NET_ADMIN"},
		"unknown":     {"NOT_A_CAP"},
		"duplicate":   {"NET_ADMIN", "NET_ADMIN"},
		"mixed":       {"NET_ADMIN", "SYS_ADMIN"},
	}
	for name, add := range cases {
		p := validCapabilityPolicy()
		p.Add = add
		err := p.Validate()
		require.Error(t, err, "case %s", name)
		assert.ErrorIs(t, err, ErrCapabilityPolicy, "case %s", name)
	}
}

func TestCapabilityPolicyAllowlistRejected(t *testing.T) {
	p := validCapabilityPolicy()
	p.AllowedApps = nil
	require.ErrorIs(t, p.Validate(), ErrCapabilityPolicy)

	p = validCapabilityPolicy()
	p.AllowedServices = nil
	require.ErrorIs(t, p.Validate(), ErrCapabilityPolicy)

	p = validCapabilityPolicy()
	p.AllowedApps = []string{"demo", "demo"}
	require.Error(t, p.Validate())

	p = validCapabilityPolicy()
	p.AllowedServices = []string{"*"}
	require.Error(t, p.Validate())
}

func TestAllowedAppCapabilitiesExcludesDangerous(t *testing.T) {
	allowed := AllowedAppCapabilities()
	for _, bad := range []string{"ALL", "SYS_ADMIN", "SYS_PTRACE", "SYS_MODULE", "SYS_RAWIO", "SYS_BOOT", "SYS_TIME", "DAC_READ_SEARCH", "BPF", "PERFMON", "MAC_ADMIN", "MAC_OVERRIDE", "AUDIT_CONTROL", "SYSLOG"} {
		assert.NotContains(t, allowed, bad)
	}
	assert.Contains(t, allowed, "NET_ADMIN")
	// The returned slice is a copy.
	allowed[0] = "mutated"
	assert.NotEqual(t, "mutated", AllowedAppCapabilities()[0])
}

func TestResolveAppCapabilitiesDenyByDefault(t *testing.T) {
	p := validCapabilityPolicy()
	_, err := p.ResolveAppCapabilities("other", "worker")
	require.ErrorIs(t, err, ErrCapabilityPolicy)
	_, err = p.ResolveAppCapabilities("demo", "helper")
	require.ErrorIs(t, err, ErrCapabilityPolicy)
}

func TestResolveAppCapabilitiesExactGrantIsCopy(t *testing.T) {
	p := validCapabilityPolicy()
	caps, err := p.ResolveAppCapabilities("demo", "worker")
	require.NoError(t, err)
	assert.Equal(t, []string{"NET_ADMIN"}, caps)
	caps[0] = "mutated"
	again, err := p.ResolveAppCapabilities("demo", "worker")
	require.NoError(t, err)
	assert.Equal(t, []string{"NET_ADMIN"}, again)
}

func TestResolveAppCapabilitiesUnionSortedDeduped(t *testing.T) {
	policies := map[string]AppCapabilityPolicy{
		"a": {Name: "a", Add: []string{"NET_ADMIN", "IPC_LOCK"}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}},
		"b": {Name: "b", Add: []string{"NET_ADMIN", "SYS_NICE"}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}},
	}
	caps, err := ResolveAppCapabilities("demo", "worker", []string{"b", "a"}, policies)
	require.NoError(t, err)
	assert.Equal(t, []string{"IPC_LOCK", "NET_ADMIN", "SYS_NICE"}, caps)
}

func TestResolveAppCapabilitiesUnknownAndEmpty(t *testing.T) {
	_, err := ResolveAppCapabilities("demo", "worker", []string{"unknown"}, nil)
	require.ErrorIs(t, err, ErrCapabilityPolicy)
	caps, err := ResolveAppCapabilities("demo", "worker", nil, nil)
	require.NoError(t, err)
	assert.Nil(t, caps)
}

func TestResolveAppCapabilitiesRefusedDoesNotEchoCaps(t *testing.T) {
	policies := map[string]AppCapabilityPolicy{"net-admin": validCapabilityPolicy()}
	_, err := ResolveAppCapabilities("other", "worker", []string{"net-admin"}, policies)
	require.ErrorIs(t, err, ErrCapabilityPolicy)
	assert.NotContains(t, err.Error(), "NET_ADMIN")
}

func TestRuntimeGuardRefusesDisallowedAddedCapabilities(t *testing.T) {
	var g RuntimeGuard
	require.NoError(t, g.Check(&ContainerConfig{AddedCapabilities: []string{"NET_ADMIN"}}))
	for _, caps := range [][]string{{"ALL"}, {"SYS_ADMIN"}, {"NET_ADMIN", "NET_ADMIN"}} {
		err := g.Check(&ContainerConfig{AddedCapabilities: caps})
		require.ErrorIs(t, err, ErrRuntimeGuard, "caps %v", caps)
	}
}

func TestAppServiceCapabilitiesValidation(t *testing.T) {
	mk := func(caps ...string) AppSpec {
		return AppSpec{Name: "demo", Services: []AppService{{Name: "worker", Image: "registry.example.com/demo/worker:1", StopGrace: 10 * time.Second, Readiness: AppReadiness{Type: "http", Path: "/healthz", Timeout: 30 * time.Second}, Capabilities: caps}}}
	}
	require.NoError(t, mk("net-admin").Validate())

	err := mk("net-admin", "net-admin").Validate()
	require.ErrorIs(t, err, ErrInvalidAppSpec)
	assert.Contains(t, err.Error(), "duplicate capability")

	for _, name := range []string{"Bad!", "NET_ADMIN", "a--b", ""} {
		err = mk(name).Validate()
		require.Error(t, err, "name %q", name)
		assert.True(t, errors.Is(err, ErrInvalidAppSpec))
	}
}

func TestDiffAppSpecCapabilities(t *testing.T) {
	svc := func(caps ...string) AppService {
		return AppService{Name: "worker", Image: "registry.example.com/demo/worker:1", StopGrace: 10 * time.Second, Readiness: AppReadiness{Type: "http", Path: "/healthz", Timeout: 30 * time.Second}, Capabilities: caps}
	}
	mk := func(s AppService) AppSpec { return AppSpec{Name: "demo", Services: []AppService{s}} }

	diff := DiffAppSpec(mk(svc()), mk(svc("net-admin")))
	assert.Contains(t, diff.Changed, "service/worker/capabilities")

	diff = DiffAppSpec(mk(svc("net-admin")), mk(svc()))
	assert.Contains(t, diff.Changed, "service/worker/capabilities")

	diff = DiffAppSpec(mk(svc("a", "b")), mk(svc("b", "a")))
	assert.NotContains(t, diff.Changed, "service/worker/capabilities", "reorder is a no-op")

	diff = DiffAppSpec(mk(svc()), mk(svc([]string{}...)))
	assert.NotContains(t, diff.Changed, "service/worker/capabilities", "nil vs empty is a no-op")
	assert.NotContains(t, diff.Changed, "service/worker/devices")
}
