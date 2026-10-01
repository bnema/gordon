package app

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/domain"
)

// AppDevicePolicy is one administrative device grant declared under
// [app_devices.<name>]. The map key is the stable logical device name an
// app manifest device references. CDI holds the explicit CDI device IDs
// granted (aggregate "=all" selectors are rejected). AllowedApps and
// AllowedServices are exact, non-empty allowlists.
type AppDevicePolicy struct {
	CDI             []string `mapstructure:"cdi"`
	AllowedApps     []string `mapstructure:"allowed_apps"`
	AllowedServices []string `mapstructure:"allowed_services"`
}

// knownAppDeviceKeys is the allowlist of [app_devices.<name>] sub-keys.
// Unknown keys (for example a typo like "cdis") fail closed instead of
// silently dropping a grant.
var knownAppDeviceKeys = map[string]struct{}{
	"cdi":              {},
	"allowed_apps":     {},
	"allowed_services": {},
}

// validateAppDeviceKeys rejects unknown sub-keys inside [app_devices.*].
// Viper unmarshals leniently, so a typo would otherwise drop the grant
// silently. settings is the decoded app_devices subtree (viper lowercases
// all keys) used only for key inspection: values still come from Config.
func validateAppDeviceKeys(devices map[string]any) error {
	for name, raw := range devices {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid app_devices.%s: entry must be a table", name)
		}
		for key := range entry {
			if _, ok := knownAppDeviceKeys[key]; !ok {
				return fmt.Errorf("invalid app_devices.%s: unknown key %q (allowed: cdi, allowed_apps, allowed_services)", name, key)
			}
		}
	}
	return nil
}

// buildAppDevicePolicies converts configured app devices into validated
// domain policies. Failures name only the device and the field-level
// reason: host inventory never appears in config errors.
func buildAppDevicePolicies(cfg Config) (map[string]domain.AppDevicePolicy, error) {
	policies := make(map[string]domain.AppDevicePolicy, len(cfg.AppDevices))
	for name, device := range cfg.AppDevices {
		policy := domain.AppDevicePolicy{
			Name:            name,
			CDI:             append([]string(nil), device.CDI...),
			AllowedApps:     append([]string(nil), device.AllowedApps...),
			AllowedServices: append([]string(nil), device.AllowedServices...),
		}
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("invalid app_devices.%s: %w: %s", name, domain.ErrDevicePolicy, redactedDevicePolicyReason(err))
		}
		policies[name] = policy
	}
	return policies, nil
}

// redactedDevicePolicyReason keeps the field-level reason from a domain
// device policy validation error while stripping every quoted value so an
// invalid config never echoes host inventory (CDI IDs, app names).
func redactedDevicePolicyReason(err error) string {
	reason := policyQuotedValue.ReplaceAllString(err.Error(), "")
	reason = strings.Join(strings.Fields(reason), " ")
	// Keep only the innermost detail: the outer wrap repeats the generic
	// violation and the policy name, which is already part of the device key.
	if idx := strings.LastIndex(reason, ": "); idx >= 0 {
		reason = reason[idx+2:]
	}
	return reason
}

// validateDeviceConfig validates the [app_devices] subtree: unknown keys
// fail closed, then the decoded policies validate. Split from
// applyLoadedConfig to keep its complexity within budget.
func validateDeviceConfig(v *viper.Viper, cfg Config) error {
	if err := validateAppDeviceKeys(v.GetStringMap("app_devices")); err != nil {
		return err
	}
	if _, err := buildAppDevicePolicies(cfg); err != nil {
		return err
	}
	return nil
}

// validateAppPolicies validates both administrative app policy subtrees
// (bind mounts and device grants) before publication. Failed validation
// keeps the previous policies live. Split from applyLoadedConfig to keep
// its complexity within budget.
func validateAppPolicies(v *viper.Viper, cfg Config) error {
	if _, err := buildAppMountPolicies(cfg); err != nil {
		return err
	}
	return validateDeviceConfig(v, cfg)
}
