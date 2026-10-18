package app

import (
	"fmt"

	"github.com/spf13/viper"

	"github.com/bnema/gordon/internal/domain"
)

// AppCapabilityPolicy is one administrative Linux capability grant declared
// under [app_capabilities.<name>]. The map key is the stable logical name an
// app manifest `capabilities` list references. Add holds the explicit
// capability names added on top of Gordon's default set (only the safe set
// documented in domain.AllowedAppCapabilities is accepted). AllowedApps and
// AllowedServices are exact, non-empty allowlists.
type AppCapabilityPolicy struct {
	Add             []string `mapstructure:"add"`
	AllowedApps     []string `mapstructure:"allowed_apps"`
	AllowedServices []string `mapstructure:"allowed_services"`
}

// knownAppCapabilityKeys is the allowlist of [app_capabilities.<name>]
// sub-keys. Unknown keys (for example a typo like "adds") fail closed
// instead of silently dropping a grant.
var knownAppCapabilityKeys = map[string]struct{}{
	"add":              {},
	"allowed_apps":     {},
	"allowed_services": {},
}

// validateAppCapabilityKeys rejects unknown sub-keys inside
// [app_capabilities.*]. Viper unmarshals leniently, so a typo would
// otherwise drop the grant silently.
func validateAppCapabilityKeys(caps map[string]any) error {
	for name, raw := range caps {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid app_capabilities.%s: entry must be a table", name)
		}
		for key := range entry {
			if _, ok := knownAppCapabilityKeys[key]; !ok {
				return fmt.Errorf("invalid app_capabilities.%s: unknown key %q (allowed: add, allowed_apps, allowed_services)", name, key)
			}
		}
	}
	return nil
}

// buildAppCapabilityPolicies converts configured capability grants into
// validated domain policies. Config errors keep the field-level reason and
// the offending capability name (a public Linux name, not host inventory) so
// the operator can fix the list; app and service names are redacted.
func buildAppCapabilityPolicies(cfg Config) (map[string]domain.AppCapabilityPolicy, error) {
	policies := make(map[string]domain.AppCapabilityPolicy, len(cfg.AppCapabilities))
	for name, grant := range cfg.AppCapabilities {
		policy := domain.AppCapabilityPolicy{
			Name:            name,
			Add:             append([]string(nil), grant.Add...),
			AllowedApps:     append([]string(nil), grant.AllowedApps...),
			AllowedServices: append([]string(nil), grant.AllowedServices...),
		}
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("invalid app_capabilities.%s: %w: %s", name, domain.ErrCapabilityPolicy, capabilityPolicyReason(err, policy))
		}
		policies[name] = policy
	}
	return policies, nil
}

// capabilityPolicyReason keeps the actionable reason of a validation error.
// Capability names in the grant are public Linux names, so a rejected
// capability keeps its name and the allowed list; every other quoted value
// (app and service names) is stripped.
func capabilityPolicyReason(err error, policy domain.AppCapabilityPolicy) string {
	if capErr := domain.ValidateAddableCapabilities(policy.Add); capErr != nil && len(policy.Add) > 0 {
		return capErr.Error()
	}
	return redactedDevicePolicyReason(err)
}

// validateCapabilityConfig validates the [app_capabilities] subtree:
// unknown keys fail closed, then the decoded policies validate.
func validateCapabilityConfig(v *viper.Viper, cfg Config) error {
	if err := validateAppCapabilityKeys(v.GetStringMap("app_capabilities")); err != nil {
		return err
	}
	_, err := buildAppCapabilityPolicies(cfg)
	return err
}
