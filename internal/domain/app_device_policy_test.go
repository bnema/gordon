package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validDevicePolicy() AppDevicePolicy {
	return AppDevicePolicy{
		Name:            "test_gpu",
		CDI:             []string{"example.com/gpu=GPU-test-uuid"},
		AllowedApps:     []string{"demo"},
		AllowedServices: []string{"worker"},
	}
}

func TestDevicePolicyValid(t *testing.T) {
	p := validDevicePolicy()
	require.NoError(t, p.Validate())
}

func TestDevicePolicyNameRejected(t *testing.T) {
	for _, name := range []string{"", "Bad", "a--b", "has space"} {
		p := validDevicePolicy()
		p.Name = name
		err := p.Validate()
		require.Error(t, err, "name %q", name)
		assert.True(t, errors.Is(err, ErrDevicePolicy))
	}
}

func TestDevicePolicyCDIRejected(t *testing.T) {
	cases := map[string][]string{
		"empty":        {},
		"empty entry":  {""},
		"raw path":     {"/dev/card0"},
		"unqualified":  {"gpu0"},
		"missing name": {"example.com/gpu="},
		"missing kind": {"=gpu0"},
		"aggregate":    {"example.com/gpu=all"},
		"duplicate":    {"example.com/gpu=0", "example.com/gpu=0"},
	}
	for name, cdi := range cases {
		p := validDevicePolicy()
		p.CDI = cdi
		err := p.Validate()
		require.Error(t, err, "case %s", name)
		assert.True(t, errors.Is(err, ErrDevicePolicy), "case %s", name)
	}
}

func TestDevicePolicyAllowlistRejected(t *testing.T) {
	p := validDevicePolicy()
	p.AllowedApps = nil
	require.Error(t, p.Validate())

	p = validDevicePolicy()
	p.AllowedApps = []string{"demo", "demo"}
	require.Error(t, p.Validate())

	p = validDevicePolicy()
	p.AllowedServices = []string{"worker", "worker"}
	require.Error(t, p.Validate())
}

func TestResolveAppDeviceDenyByDefault(t *testing.T) {
	p := validDevicePolicy()
	_, err := p.ResolveAppDevice("other", "worker", "test_gpu")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDevicePolicy))

	_, err = p.ResolveAppDevice("demo", "helper", "test_gpu")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDevicePolicy))
}

func TestResolveAppDeviceExactGrant(t *testing.T) {
	p := validDevicePolicy()
	ids, err := p.ResolveAppDevice("demo", "worker", "test_gpu")
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com/gpu=GPU-test-uuid"}, ids)
	// The returned slice must be a copy: mutating it must not affect
	// later resolutions.
	ids[0] = "mutated"
	again, err := p.ResolveAppDevice("demo", "worker", "test_gpu")
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com/gpu=GPU-test-uuid"}, again)
}

func TestResolveAppDevicesUnknownDenied(t *testing.T) {
	policies := map[string]AppDevicePolicy{"test_gpu": validDevicePolicy()}
	_, err := ResolveAppDevices("demo", "worker", []string{"unknown"}, policies)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDevicePolicy))
}

func TestResolveAppDevicesDeterministicUnion(t *testing.T) {
	policies := map[string]AppDevicePolicy{
		"b": {Name: "b", CDI: []string{"example.com/gpu=GPU-b"}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}},
		"a": {Name: "a", CDI: []string{"example.com/gpu=GPU-a"}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}},
	}
	ids, err := ResolveAppDevices("demo", "worker", []string{"b", "a"}, policies)
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com/gpu=GPU-a", "example.com/gpu=GPU-b"}, ids)
}

func TestResolveAppDevicesDuplicateIDRejected(t *testing.T) {
	shared := "example.com/gpu=GPU-shared"
	policies := map[string]AppDevicePolicy{
		"a": {Name: "a", CDI: []string{shared}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}},
		"b": {Name: "b", CDI: []string{shared}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}},
	}
	_, err := ResolveAppDevices("demo", "worker", []string{"a", "b"}, policies)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDevicePolicy))
}

func TestResolveAppDevicesEmptyIsNil(t *testing.T) {
	ids, err := ResolveAppDevices("demo", "worker", nil, nil)
	require.NoError(t, err)
	assert.Nil(t, ids)
}

func TestValidateDeviceName(t *testing.T) {
	require.NoError(t, ValidateDeviceName("test_gpu"))
	for _, name := range []string{"", "Bad", "a--b", "/dev/card0"} {
		require.Error(t, ValidateDeviceName(name), "name %q", name)
	}
}

func TestAppServiceDevicesValidation(t *testing.T) {
	base := AppSpec{Name: "demo", Services: []AppService{{Name: "worker", Image: "registry.example.com/demo/worker:1", StopGrace: 10 * time.Second, Readiness: AppReadiness{Type: "http", Path: "/healthz", Timeout: 30 * time.Second}}}}
	valid := base
	valid.Services[0].Devices = []string{"test_gpu"}
	require.NoError(t, valid.Validate())

	dup := base
	dup.Services[0].Devices = []string{"test_gpu", "test_gpu"}
	err := dup.Validate()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidAppSpec))
	assert.Contains(t, err.Error(), "duplicate device")

	bad := base
	bad.Services[0].Devices = []string{"Bad!"}
	err = bad.Validate()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidAppSpec))
}

func TestDiffAppSpecDevices(t *testing.T) {
	svc := func(devices ...string) AppService {
		return AppService{Name: "worker", Image: "registry.example.com/demo/worker:1", StopGrace: 10 * time.Second, Readiness: AppReadiness{Type: "http", Path: "/healthz", Timeout: 30 * time.Second}, Devices: devices}
	}
	mk := func(services ...AppService) AppSpec { return AppSpec{Name: "demo", Services: services} }

	// Add/remove report service/worker/devices.
	oldSpec, nextSpec := mk(svc()), mk(svc("test_gpu"))
	diff := DiffAppSpec(oldSpec, nextSpec)
	assert.Contains(t, diff.Changed, "service/worker/devices")

	// Reorder-only is a no-op.
	oldSpec, nextSpec = mk(svc("a", "b")), mk(svc("b", "a"))
	diff = DiffAppSpec(oldSpec, nextSpec)
	assert.NotContains(t, diff.Changed, "service/worker/devices")

	// Nil vs empty stays a no-op.
	oldSpec, nextSpec = mk(svc()), mk(svc([]string{}...))
	diff = DiffAppSpec(oldSpec, nextSpec)
	assert.NotContains(t, diff.Changed, "service/worker/devices")
}
