package app

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bnema/gordon/internal/domain"
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
		name   string
		mount  AppMountPolicy
		reason string
	}{
		{"empty source", AppMountPolicy{AllowedApps: []string{"blog"}, AllowedServices: []string{"web"}}, "source must not be empty"},
		{"relative source", AppMountPolicy{Source: "binds", AllowedApps: []string{"blog"}, AllowedServices: []string{"web"}}, "source must be absolute"},
		{"empty allowed apps", AppMountPolicy{Source: "/srv/binds", AllowedServices: []string{"web"}}, "allowed apps must not be empty"},
		{"empty allowed services", AppMountPolicy{Source: "/srv/binds", AllowedApps: []string{"blog"}}, "allowed services must not be empty"},
		{"unclean root", AppMountPolicy{Source: "/srv/binds", AllowedApps: []string{"blog"}, AllowedServices: []string{"web"}, Root: "/srv/../etc"}, "root must be a normalized clean path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{AppMounts: map[string]AppMountPolicy{"config": tc.mount}}
			_, err := buildAppMountPolicies(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "app_mounts.config")
			assert.Contains(t, err.Error(), tc.reason, "the field-level reason must survive redaction")
			assert.ErrorIs(t, err, domain.ErrBindPolicy)
			if tc.mount.Source != "" {
				assert.NotContains(t, err.Error(), tc.mount.Source, "config errors must not echo the source path")
			}
			if tc.mount.Root != "" {
				assert.NotContains(t, err.Error(), tc.mount.Root, "config errors must not echo the root path")
			}
		})
	}
}

func TestRedactedBindPolicyReason_RedactsConfiguredValues(t *testing.T) {
	_, err := buildAppMountPolicies(Config{AppMounts: map[string]AppMountPolicy{
		"config": {
			Source:          "/srv/binds/blog/config",
			AllowedApps:     []string{"Blog"},
			AllowedServices: []string{"web"},
		},
	}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "app name must be a DNS label")
	assert.NotContains(t, err.Error(), "/srv/binds/blog/config", "configured paths must never be echoed")
	assert.NotContains(t, err.Error(), "Blog", "configured names must never be echoed")
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
