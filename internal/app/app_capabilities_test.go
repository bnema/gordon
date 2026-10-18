package app

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
)

func readCapConfig(t *testing.T, doc string) (*viper.Viper, Config) {
	t.Helper()
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))
	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))
	return v, cfg
}

func TestBuildAppCapabilityPolicies_ValidConfig(t *testing.T) {
	v, cfg := readCapConfig(t, `
[app_capabilities.vm-net]
add = ["NET_ADMIN"]
allowed_apps = ["myapp"]
allowed_services = ["worker"]
`)
	require.NoError(t, validateCapabilityConfig(v, cfg))
	policies, err := buildAppCapabilityPolicies(cfg)
	require.NoError(t, err)
	policy := policies["vm-net"]
	assert.Equal(t, "vm-net", policy.Name)
	assert.Equal(t, []string{"NET_ADMIN"}, policy.Add)
	assert.Equal(t, []string{"myapp"}, policy.AllowedApps)
	assert.Equal(t, []string{"worker"}, policy.AllowedServices)
}

func TestBuildAppCapabilityPolicies_Invalid(t *testing.T) {
	apps, svcs := []string{"demo"}, []string{"worker"}
	cases := []struct {
		name   string
		grant  AppCapabilityPolicy
		reason string
	}{
		{"empty add", AppCapabilityPolicy{AllowedApps: apps, AllowedServices: svcs}, "add must not be empty"},
		{"ALL", AppCapabilityPolicy{Add: []string{"ALL"}, AllowedApps: apps, AllowedServices: svcs}, "not allowed"},
		{"SYS_ADMIN", AppCapabilityPolicy{Add: []string{"SYS_ADMIN"}, AllowedApps: apps, AllowedServices: svcs}, "SYS_ADMIN"},
		{"duplicate", AppCapabilityPolicy{Add: []string{"NET_ADMIN", "NET_ADMIN"}, AllowedApps: apps, AllowedServices: svcs}, "duplicate"},
		{"empty apps", AppCapabilityPolicy{Add: []string{"NET_ADMIN"}, AllowedServices: svcs}, "allowed apps must not be empty"},
		{"empty services", AppCapabilityPolicy{Add: []string{"NET_ADMIN"}, AllowedApps: apps}, "allowed services must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildAppCapabilityPolicies(Config{AppCapabilities: map[string]AppCapabilityPolicy{"net": tc.grant}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "app_capabilities.net")
			assert.Contains(t, err.Error(), tc.reason)
			assert.ErrorIs(t, err, domain.ErrCapabilityPolicy)
			assert.NotContains(t, err.Error(), "demo", "app names are redacted")
			assert.NotContains(t, err.Error(), "worker", "service names are redacted")
		})
	}
}

func TestValidateAppCapabilityKeys_UnknownKey(t *testing.T) {
	v, _ := readCapConfig(t, `
[app_capabilities.net]
adds = ["NET_ADMIN"]
allowed_apps = ["demo"]
allowed_services = ["worker"]
`)
	err := validateAppCapabilityKeys(v.GetStringMap("app_capabilities"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key")
	require.NoError(t, validateAppCapabilityKeys(nil))
}

func TestValidateAppPolicies_IncludesCapabilities(t *testing.T) {
	v, cfg := readCapConfig(t, `
[app_capabilities.net]
add = ["SYS_ADMIN"]
allowed_apps = ["demo"]
allowed_services = ["worker"]
`)
	require.ErrorIs(t, validateAppPolicies(v, cfg), domain.ErrCapabilityPolicy)
}

func TestBuildAppCapabilityPolicies_CopiesSlicesAndDefaultsEmpty(t *testing.T) {
	policies, err := buildAppCapabilityPolicies(Config{})
	require.NoError(t, err)
	assert.Empty(t, policies)

	cfg := Config{AppCapabilities: map[string]AppCapabilityPolicy{
		"net": {Add: []string{"NET_ADMIN"}, AllowedApps: []string{"demo"}, AllowedServices: []string{"worker"}},
	}}
	policies, err = buildAppCapabilityPolicies(cfg)
	require.NoError(t, err)
	policies["net"].Add[0] = "mutated"
	assert.Equal(t, []string{"NET_ADMIN"}, cfg.AppCapabilities["net"].Add)
}
