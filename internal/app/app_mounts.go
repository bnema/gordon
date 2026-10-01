package app

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// AppMountPolicy is one administrative bind policy declared under
// [[app_mounts.<name>]]. The map key is the stable mount name an app manifest
// bind references. Source is the only host path a bind may come from. Root,
// when set, is the administrative boundary the resolved source must stay
// under. AllowedApps and AllowedServices are exact, non-empty allowlists.
type AppMountPolicy struct {
	Source          string   `mapstructure:"source"`
	ReadOnly        bool     `mapstructure:"read_only"`
	AllowedApps     []string `mapstructure:"allowed_apps"`
	AllowedServices []string `mapstructure:"allowed_services"`
	Root            string   `mapstructure:"root"`
}

// policyQuotedValue matches the quoted values domain validation embeds in
// its messages, such as configured source paths or roots.
var policyQuotedValue = regexp.MustCompile(`"[^"]*"`)

// redactedBindPolicyReason keeps the field-level reason from a domain policy
// validation error (for example "source must be absolute") while stripping
// every quoted value so an invalid config never echoes host paths.
func redactedBindPolicyReason(err error) string {
	reason := policyQuotedValue.ReplaceAllString(err.Error(), "")
	reason = strings.Join(strings.Fields(reason), " ")
	// Keep only the innermost detail: the outer wrap repeats the generic
	// violation and the policy name, which is already part of the mount key.
	if idx := strings.LastIndex(reason, ": "); idx >= 0 {
		reason = reason[idx+2:]
	}
	return reason
}

// buildAppMountPolicies converts configured app mounts into validated domain
// policies. Failures name only the mount and the field-level reason: source
// paths are never echoed.
func buildAppMountPolicies(cfg Config) (map[string]domain.AppBindPolicy, error) {
	policies := make(map[string]domain.AppBindPolicy, len(cfg.AppMounts))
	for name, mount := range cfg.AppMounts {
		root := mount.Root
		if root == "" && filepath.IsAbs(mount.Source) {
			// The source's parent is the least surprising implicit
			// administrative boundary for the documented minimal form.
			// A symlink source may resolve within it, never escape it.
			root = filepath.Dir(mount.Source)
		}
		policy := domain.AppBindPolicy{
			Name:            name,
			Source:          mount.Source,
			ReadOnly:        mount.ReadOnly,
			AllowedApps:     mount.AllowedApps,
			AllowedServices: mount.AllowedServices,
			Root:            root,
		}
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("invalid app_mounts.%s: %w: %s", name, domain.ErrBindPolicy, redactedBindPolicyReason(err))
		}
		policies[name] = policy
	}
	return policies, nil
}
