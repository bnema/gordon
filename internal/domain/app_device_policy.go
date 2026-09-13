package domain

import (
	"fmt"
	"slices"
	"strings"
)

// AppDevicePolicy is one named administrative device grant declared under
// [app_devices.<name>]. The installation maps a logical device name to
// explicit CDI device IDs and exact app/service allowlists. App manifests
// request devices by logical name; Gordon resolves the CDI IDs at
// activation time. Host CDI resolution is never persisted as desired
// intent: revisions keep the logical names.
type AppDevicePolicy struct {
	Name string
	// CDI holds the explicit CDI device IDs (for example
	// "nvidia.com/gpu=GPU-..."). Aggregate selectors such as "=all"
	// are rejected: every granted device must be named explicitly.
	CDI []string
	// AllowedApps and AllowedServices are exact, non-empty allowlists.
	AllowedApps     []string
	AllowedServices []string
}

// ValidateDeviceName checks a logical device name (service charset plus -- ban).
func ValidateDeviceName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("%w: device name %q must match [a-z0-9_.-], max 63", ErrInvalidAppSpec, name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("%w: device name %q must not contain -- (reserved separator)", ErrInvalidAppSpec, name)
	}
	return nil
}

// Validate checks the policy shape without touching the runtime.
func (p AppDevicePolicy) Validate() error {
	if err := ValidateDevicePolicyName(p.Name); err != nil {
		return err
	}
	if err := validateDeviceCDIIDs(p.Name, p.CDI); err != nil {
		return err
	}
	if err := validateAllowlist("app", p.AllowedApps, ValidateAppName); err != nil {
		return fmt.Errorf("%w: device policy %q: %v", ErrDevicePolicy, p.Name, err)
	}
	if err := validateAllowlist("service", p.AllowedServices, ValidateServiceName); err != nil {
		return fmt.Errorf("%w: device policy %q: %v", ErrDevicePolicy, p.Name, err)
	}
	return nil
}

// ValidateDevicePolicyName checks a stable policy name (service charset plus -- ban).
func ValidateDevicePolicyName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("%w: device policy name %q must match [a-z0-9_.-], max 63", ErrDevicePolicy, name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("%w: device policy name %q must not contain -- (reserved separator)", ErrDevicePolicy, name)
	}
	return nil
}

// validateDeviceCDIIDs requires a non-empty list of unique, well-formed CDI
// device IDs. Raw /dev paths, empty entries, and aggregate "=all"
// selectors are rejected.
func validateDeviceCDIIDs(policy string, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("%w: device policy %q: cdi must not be empty", ErrDevicePolicy, policy)
	}
	seen := map[string]struct{}{}
	for _, id := range ids {
		if err := validateDeviceCDIID(id); err != nil {
			return fmt.Errorf("%w: device policy %q: %v", ErrDevicePolicy, policy, err)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: device policy %q: duplicate cdi device %q", ErrDevicePolicy, policy, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// validateDeviceCDIID checks one qualified CDI device ID of the form
// "<domain>/<kind>=<name>" (for example "nvidia.com/gpu=GPU-...").
// The device name must not be empty, "all", or a host path.
func validateDeviceCDIID(id string) error {
	if id == "" {
		return fmt.Errorf("cdi device must not be empty")
	}
	if strings.HasPrefix(id, "/") {
		return fmt.Errorf("cdi device %q must be a qualified CDI ID, not a host path", id)
	}
	kind, name, ok := strings.Cut(id, "=")
	if !ok || kind == "" || name == "" {
		return fmt.Errorf("cdi device %q must be a qualified CDI ID of the form \"<domain>/<kind>=<name>\"", id)
	}
	if !strings.Contains(kind, "/") {
		return fmt.Errorf("cdi device %q must be a qualified CDI ID of the form \"<domain>/<kind>=<name>\"", id)
	}
	if name == "all" {
		return fmt.Errorf("cdi device %q: aggregate \"=all\" selectors are not allowed, name each device explicitly", id)
	}
	return nil
}

// ResolveAppDevice resolves the logical device grant under this policy
// for the given app and service. It validates the policy shape, enforces
// the exact app/service allowlists, and returns the explicit CDI IDs.
// The result is runtime-ready: callers encode the IDs as one native CDI
// DeviceRequest.
func (p AppDevicePolicy) ResolveAppDevice(app, service string) ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !allowlistContains(p.AllowedApps, app) {
		return nil, fmt.Errorf("%w: device policy %q does not allow app %q", ErrDevicePolicy, p.Name, app)
	}
	if !allowlistContains(p.AllowedServices, service) {
		return nil, fmt.Errorf("%w: device policy %q does not allow service %q", ErrDevicePolicy, p.Name, service)
	}
	return slices.Clone(p.CDI), nil
}

// ResolveAppDevices resolves every logical device in names against the
// given policies for one app service. It returns the deterministic union
// of CDI IDs (sorted) and rejects duplicate resolved IDs across logical
// resources. Errors name only the app, service, and logical resource:
// host inventory never appears.
func ResolveAppDevices(app, service string, names []string, policies map[string]AppDevicePolicy) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var resolved []string
	seen := map[string]struct{}{}
	for _, name := range names {
		policy, ok := policies[name]
		if !ok {
			return nil, fmt.Errorf("%w: app %q service %q device %q: no administrative device policy configured", ErrDevicePolicy, app, service, name)
		}
		ids, err := policy.ResolveAppDevice(app, service)
		if err != nil {
			return nil, fmt.Errorf("%w: app %q service %q device %q: refused by administrative device policy", ErrDevicePolicy, app, service, name)
		}
		for _, id := range ids {
			if _, dup := seen[id]; dup {
				return nil, fmt.Errorf("%w: app %q service %q device %q: cdi device already granted by another device", ErrDevicePolicy, app, service, name)
			}
			seen[id] = struct{}{}
			resolved = append(resolved, id)
		}
	}
	slices.Sort(resolved)
	return resolved, nil
}
