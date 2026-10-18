package domain

import (
	"fmt"
	"slices"
	"strings"
)

// allowedAppCapabilities is the closed set of Linux capabilities an
// administrative capability policy may add to an app service. It is the
// Docker/Podman default container set plus the few extra capabilities an
// unprivileged network or VM workload legitimately needs:
//
//	NET_ADMIN                 configure interfaces, TAP devices, nftables
//	                          inside the container's own network namespace
//	IPC_LOCK                  lock guest memory (mlock)
//	SYS_NICE, SYS_RESOURCE    scheduling priority and rlimit overrides
//
// Everything else is refused. In particular ALL, SYS_ADMIN, SYS_PTRACE,
// SYS_MODULE, SYS_RAWIO, SYS_BOOT, SYS_TIME, DAC_READ_SEARCH, BPF,
// PERFMON, MAC_ADMIN, MAC_OVERRIDE, AUDIT_CONTROL, and SYSLOG can reach
// the host or defeat container confinement and have no policy form: a
// workload that truly needs one is not an unprivileged service.
var allowedAppCapabilities = []string{
	"AUDIT_WRITE",
	"CHOWN",
	"DAC_OVERRIDE",
	"FOWNER",
	"FSETID",
	"IPC_LOCK",
	"KILL",
	"MKNOD",
	"NET_ADMIN",
	"NET_BIND_SERVICE",
	"NET_RAW",
	"SETFCAP",
	"SETGID",
	"SETPCAP",
	"SETUID",
	"SYS_CHROOT",
	"SYS_NICE",
	"SYS_RESOURCE",
}

// defaultAppCapAdd is Gordon's base capability add set for app containers
// (on top of cap_drop = ALL). Granted capabilities are added to it.
var defaultAppCapAdd = []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETUID", "SETGID", "NET_BIND_SERVICE"}

// DefaultAppCapAdd returns Gordon's base capability add set. The slice is a
// copy.
func DefaultAppCapAdd() []string {
	return slices.Clone(defaultAppCapAdd)
}

// NormalizeCapability returns the canonical form of a capability name as
// reported by an engine: upper case without the CAP_ prefix.
func NormalizeCapability(name string) string {
	return strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(name)), "CAP_")
}

// ExcessCapabilities returns the sorted, normalized capabilities in actual
// that are not in authorized. Both sides are normalized first, so engines
// that report CAP_ prefixed names compare equal. A container holding fewer
// capabilities than authorized is not excess.
func ExcessCapabilities(actual, authorized []string) []string {
	allowed := make(map[string]struct{}, len(authorized))
	for _, name := range authorized {
		allowed[NormalizeCapability(name)] = struct{}{}
	}
	var excess []string
	for _, name := range actual {
		n := NormalizeCapability(name)
		if _, ok := allowed[n]; !ok {
			excess = append(excess, n)
		}
	}
	slices.Sort(excess)
	return slices.Compact(excess)
}

// AllowedAppCapabilities returns the sorted capability names a policy may
// add. The slice is a copy.
func AllowedAppCapabilities() []string {
	return slices.Clone(allowedAppCapabilities)
}

// AppCapabilityPolicy is one named administrative capability grant
// declared under [app_capabilities.<name>]. The installation maps a
// logical name to explicit Linux capabilities and exact app/service
// allowlists. App manifests request capabilities by logical name; Gordon
// resolves them at activation time and adds them to the container on top
// of its default capability set. Resolution is never persisted: revisions
// keep the logical names.
type AppCapabilityPolicy struct {
	Name string
	// Add holds the explicit capability names granted (for example
	// "NET_ADMIN"), restricted to the allowed set.
	Add []string
	// AllowedApps and AllowedServices are exact, non-empty allowlists.
	AllowedApps     []string
	AllowedServices []string
}

// ValidateCapabilityPolicyRef checks a logical capability-policy name used
// from a manifest (service charset plus -- ban).
func ValidateCapabilityPolicyRef(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("%w: capability name %q must match [a-z0-9_.-], max 63", ErrInvalidAppSpec, name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("%w: capability name %q must not contain -- (reserved separator)", ErrInvalidAppSpec, name)
	}
	return nil
}

// ValidateCapabilityPolicyName checks a stable policy name (service
// charset plus -- ban).
func ValidateCapabilityPolicyName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("%w: capability policy name %q must match [a-z0-9_.-], max 63", ErrCapabilityPolicy, name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("%w: capability policy name %q must not contain -- (reserved separator)", ErrCapabilityPolicy, name)
	}
	return nil
}

// Validate checks the policy shape without touching the runtime.
func (p AppCapabilityPolicy) Validate() error {
	if err := ValidateCapabilityPolicyName(p.Name); err != nil {
		return err
	}
	if err := validateCapabilityNames(p.Add); err != nil {
		return fmt.Errorf("%w: capability policy %q: %v", ErrCapabilityPolicy, p.Name, err)
	}
	if err := validateAllowlist("app", p.AllowedApps, ValidateAppName); err != nil {
		return fmt.Errorf("%w: capability policy %q: %w", ErrCapabilityPolicy, p.Name, err)
	}
	if err := validateAllowlist("service", p.AllowedServices, ValidateServiceName); err != nil {
		return fmt.Errorf("%w: capability policy %q: %w", ErrCapabilityPolicy, p.Name, err)
	}
	return nil
}

// validateCapabilityNames requires a non-empty list of unique names from
// the allowed set.
func validateCapabilityNames(names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("add must not be empty")
	}
	return ValidateAddableCapabilities(names)
}

// ValidateAddableCapabilities checks that every name is in the allowed set
// and unique. An empty list is valid. It is also the runtime guard's check
// on resolved capabilities, whatever produced them.
func ValidateAddableCapabilities(names []string) error {
	seen := map[string]struct{}{}
	for _, name := range names {
		if name == "" {
			return fmt.Errorf("capability must not be empty")
		}
		if !slices.Contains(allowedAppCapabilities, name) {
			return fmt.Errorf("capability %q is not allowed (allowed: %s)", name, strings.Join(allowedAppCapabilities, ", "))
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("duplicate capability %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// ResolveAppCapabilities resolves the logical grant under this policy for
// the given app and service. It validates the policy shape, enforces the
// exact app/service allowlists, and returns the explicit capabilities.
func (p AppCapabilityPolicy) ResolveAppCapabilities(app, service string) ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !allowlistContains(p.AllowedApps, app) {
		return nil, fmt.Errorf("%w: capability policy %q does not allow app %q", ErrCapabilityPolicy, p.Name, app)
	}
	if !allowlistContains(p.AllowedServices, service) {
		return nil, fmt.Errorf("%w: capability policy %q does not allow service %q", ErrCapabilityPolicy, p.Name, service)
	}
	return slices.Clone(p.Add), nil
}

// ResolveAppCapabilities resolves every logical capability policy in names
// for one app service. It returns the deterministic, de-duplicated union
// (sorted): two policies granting the same capability is harmless. Errors
// name only the app, service, and logical policy.
func ResolveAppCapabilities(app, service string, names []string, policies map[string]AppCapabilityPolicy) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	var resolved []string
	for _, name := range names {
		policy, ok := policies[name]
		if !ok {
			return nil, fmt.Errorf("%w: app %q service %q capability %q: no administrative capability policy configured", ErrCapabilityPolicy, app, service, name)
		}
		caps, err := policy.ResolveAppCapabilities(app, service)
		if err != nil {
			return nil, fmt.Errorf("%w: app %q service %q capability %q: refused by administrative capability policy", ErrCapabilityPolicy, app, service, name)
		}
		resolved = append(resolved, caps...)
	}
	slices.Sort(resolved)
	return slices.Compact(resolved), nil
}
