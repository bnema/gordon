package app

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAppMountPolicies_ValidConfigUnmarshal(t *testing.T) {
	v := viper.New()
	v.SetConfigType("toml")
	require.NoError(t, v.ReadConfig(strings.NewReader(`
[app_mounts.config]
source = "/srv/gordon/binds/blog/config"
read_only = true
allowed_apps = ["blog", "docs"]
allowed_services = ["web"]
root = "/srv/gordon"
`)))

	var cfg Config
	require.NoError(t, v.Unmarshal(&cfg))

	policies, err := buildAppMountPolicies(cfg)
	require.NoError(t, err)
	require.Contains(t, policies, "config")
	policy := policies["config"]
	assert.Equal(t, "config", policy.Name)
	assert.Equal(t, "/srv/gordon/binds/blog/config", policy.Source)
	assert.True(t, policy.ReadOnly)
	assert.Equal(t, []string{"blog", "docs"}, policy.AllowedApps)
	assert.Equal(t, []string{"web"}, policy.AllowedServices)
	assert.Equal(t, "/srv/gordon", policy.Root)
}

func TestBuildAppMountPolicies_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		mount AppMountPolicy
	}{
		{"empty source", AppMountPolicy{AllowedApps: []string{"blog"}, AllowedServices: []string{"web"}}},
		{"relative source", AppMountPolicy{Source: "binds", AllowedApps: []string{"blog"}, AllowedServices: []string{"web"}}},
		{"empty allowed apps", AppMountPolicy{Source: "/srv/binds", AllowedServices: []string{"web"}}},
		{"empty allowed services", AppMountPolicy{Source: "/srv/binds", AllowedApps: []string{"blog"}}},
		{"unclean root", AppMountPolicy{Source: "/srv/binds", AllowedApps: []string{"blog"}, AllowedServices: []string{"web"}, Root: "/srv/../etc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{AppMounts: map[string]AppMountPolicy{"config": tc.mount}}
			_, err := buildAppMountPolicies(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "app_mounts.config")
			if tc.mount.Source != "" {
				assert.NotContains(t, err.Error(), tc.mount.Source, "config errors must not echo the source path")
			}
		})
	}
}

func TestBuildAppMountPolicies_DefaultRootIsSourceParent(t *testing.T) {
	cfg := Config{AppMounts: map[string]AppMountPolicy{
		"config": {
			Source:          "/srv/binds/config",
			AllowedApps:     []string{"blog"},
			AllowedServices: []string{"web"},
		},
	}}
	policies, err := buildAppMountPolicies(cfg)
	require.NoError(t, err)
	assert.Equal(t, "/srv/binds", policies["config"].Root)
}

func TestConfig_AppMountsDefaultsEmpty(t *testing.T) {
	policies, err := buildAppMountPolicies(Config{})
	require.NoError(t, err)
	assert.Empty(t, policies)
}
