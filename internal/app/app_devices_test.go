package app

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func TestBuildAppDevicePolicies_ValidConfigUnmarshal(t *testing.T) {
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
[app_devices.test_gpu]
cdi = ["example.com/gpu=GPU-test-uuid"]
allowed_apps = ["demo"]
allowed_services = ["worker"]
`)))

	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	require.NoError(t, validateAppDeviceKeys(v.GetStringMap("app_devices")))

	policies, err := buildAppDevicePolicies(cfg)
	require.NoError(t, err)
	require.Contains(t, policies, "test_gpu")
	policy := policies["test_gpu"]
	assert.Equal(t, "test_gpu", policy.Name)
	assert.Equal(t, []string{"example.com/gpu=GPU-test-uuid"}, policy.CDI)
	assert.Equal(t, []string{"demo"}, policy.AllowedApps)
	assert.Equal(t, []string{"worker"}, policy.AllowedServices)
}

func TestBuildAppDevicePolicies_Invalid(t *testing.T) {
	cases := []struct {
		name   string
		device AppDevicePolicy
		reason string
	}{
		{"empty cdi", AppDevicePolicy{AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}}, "cdi must not be empty"},
		{"raw path", AppDevicePolicy{CDI: []string{"/dev/card0"}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}}, "not a host path"},
		{"aggregate", AppDevicePolicy{CDI: []string{"example.com/gpu=all"}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}}, "not allowed"},
		{"empty allowed apps", AppDevicePolicy{CDI: []string{"example.com/gpu=0"}, AllowedServices: []string{"worker"}}, "allowed apps must not be empty"},
		{"empty allowed services", AppDevicePolicy{CDI: []string{"example.com/gpu=0"}, AllowedApps: []string{"demo"}}, "allowed services must not be empty"},
		{"duplicate cdi", AppDevicePolicy{CDI: []string{"example.com/gpu=0", "example.com/gpu=0"}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}}, "duplicate cdi device"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{AppDevices: map[string]AppDevicePolicy{"test_gpu": tc.device}}
			_, err := buildAppDevicePolicies(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "app_devices.test_gpu")
			assert.Contains(t, err.Error(), tc.reason, "the field-level reason must survive redaction")
			assert.ErrorIs(t, err, domain.ErrDevicePolicy)
			for _, id := range tc.device.CDI {
				assert.NotContains(t, err.Error(), id, "config errors must not echo CDI IDs")
			}
		})
	}
}

func TestValidateAppDeviceKeys_UnknownKey(t *testing.T) {
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
[app_devices.test_gpu]
cdis = ["example.com/gpu=0"]
allowed_apps = ["demo"]
allowed_services = ["worker"]
`)))

	err := validateAppDeviceKeys(v.GetStringMap("app_devices"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key")
	assert.Contains(t, err.Error(), "app_devices.test_gpu")
}

func TestValidateAppDeviceKeys_Valid(t *testing.T) {
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
[app_devices.test_gpu]
cdi = ["example.com/gpu=0"]
allowed_apps = ["demo"]
allowed_services = ["worker"]
`)))

	require.NoError(t, validateAppDeviceKeys(v.GetStringMap("app_devices")))
	require.NoError(t, validateAppDeviceKeys(nil))
}

func TestBuildAppDevicePolicies_CopiesSlices(t *testing.T) {
	cfg := Config{AppDevices: map[string]AppDevicePolicy{
		"test_gpu": {
			CDI:             []string{"example.com/gpu=0"},
			AllowedApps:     []string{"demo"},
			AllowedServices: []string{"worker"},
		},
	}}
	policies, err := buildAppDevicePolicies(cfg)
	require.NoError(t, err)
	// Mutating the published policy must not affect later builds from the
	// same config: reloads always rebuild from Config.
	policies["test_gpu"].CDI[0] = "mutated"
	again, err := buildAppDevicePolicies(cfg)
	require.NoError(t, err)
	assert.Equal(t, []string{"example.com/gpu=0"}, again["test_gpu"].CDI)
}

func TestConfig_AppDevicesDefaultsEmpty(t *testing.T) {
	policies, err := buildAppDevicePolicies(Config{})
	require.NoError(t, err)
	assert.Empty(t, policies)
}
