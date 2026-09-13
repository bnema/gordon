package domain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppBindPolicy is one named administrative bind policy. The installation
// declares policies; app manifests reference them by name. Source is the
// only host location a bind governed by this policy may come from. Root,
// when set, is the administrative boundary the resolved source must remain
// under. AllowedApps and AllowedServices are exact, non-empty allowlists.
type AppBindPolicy struct {
	Name            string
	Source          string
	ReadOnly        bool
	AllowedApps     []string
	AllowedServices []string
	Root            string
}

// ResolvedAppBind is a bind whose source has been resolved and verified for
// the runtime to mount. It carries no logging, DTO, or transport concerns.
type ResolvedAppBind struct {
	Name        string
	Source      string
	Destination string
	ReadOnly    bool
}

// ValidateBindPolicyName checks a stable policy name (service charset plus -- ban).
func ValidateBindPolicyName(name string) error {
	if !serviceNamePattern.MatchString(name) {
		return fmt.Errorf("%w: policy name %q must match [a-z0-9_.-], max 63", ErrBindPolicy, name)
	}
	if strings.Contains(name, "--") {
		return fmt.Errorf("%w: policy name %q must not contain -- (reserved separator)", ErrBindPolicy, name)
	}
	return nil
}

// Validate checks the policy shape without touching the filesystem.
func (p AppBindPolicy) Validate() error {
	if err := ValidateBindPolicyName(p.Name); err != nil {
		return err
	}
	if err := validatePolicyPath("source", p.Source); err != nil {
		return fmt.Errorf("%w: policy %q: %v", ErrBindPolicy, p.Name, err)
	}
	if p.Root != "" {
		if err := validatePolicyPath("root", p.Root); err != nil {
			return fmt.Errorf("%w: policy %q: %v", ErrBindPolicy, p.Name, err)
		}
	}
	if err := validateAllowlist("app", p.AllowedApps, ValidateAppName); err != nil {
		return fmt.Errorf("%w: policy %q: %v", ErrBindPolicy, p.Name, err)
	}
	if err := validateAllowlist("service", p.AllowedServices, ValidateServiceName); err != nil {
		return fmt.Errorf("%w: policy %q: %v", ErrBindPolicy, p.Name, err)
	}
	return nil
}

// validatePolicyPath requires a non-empty, absolute, normalized host path.
func validatePolicyPath(kind, value string) error {
	if value == "" {
		return fmt.Errorf("%s must not be empty", kind)
	}
	if !filepath.IsAbs(value) {
		return fmt.Errorf("%s %q must be absolute", kind, value)
	}
	if filepath.Clean(value) != value {
		return fmt.Errorf("%s %q must be a normalized clean path", kind, value)
	}
	if kind == "source" && value == string(filepath.Separator) {
		return fmt.Errorf("source must not be the filesystem root")
	}
	return nil
}

// validateAllowlist requires a non-empty exact list of valid, unique names.
func validateAllowlist(kind string, names []string, validate func(string) error) error {
	if len(names) == 0 {
		return fmt.Errorf("allowed %ss must not be empty", kind)
	}
	seen := map[string]struct{}{}
	for _, name := range names {
		if err := validate(name); err != nil {
			return fmt.Errorf("allowed %s %q: %v", kind, name, err)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("allowed %s %q is duplicated", kind, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// ResolveAppBind resolves one AppBind under this policy for the given app and
// service. It validates the policy and bind shapes, enforces the exact
// app/service allowlists, resolves the source through symlinks, verifies it is
// a regular file or directory that remains under the configured root, and
// never weakens read-only intent (policy read-only and bind read-only both
// force the resolved bind read-only). The result is runtime-ready: callers
// mount ResolvedAppBind.Source at Destination.
func (p AppBindPolicy) ResolveAppBind(app, service string, bind AppBind) (ResolvedAppBind, error) {
	if err := p.Validate(); err != nil {
		return ResolvedAppBind{}, err
	}
	if !allowlistContains(p.AllowedApps, app) {
		return ResolvedAppBind{}, fmt.Errorf("%w: policy %q does not allow app %q", ErrBindPolicy, p.Name, app)
	}
	if !allowlistContains(p.AllowedServices, service) {
		return ResolvedAppBind{}, fmt.Errorf("%w: policy %q does not allow service %q", ErrBindPolicy, p.Name, service)
	}
	if err := ValidateBindName(bind.Name); err != nil {
		return ResolvedAppBind{}, err
	}
	if err := ValidateBindDestination(bind.Path); err != nil {
		return ResolvedAppBind{}, err
	}
	source, err := p.resolveSource()
	if err != nil {
		return ResolvedAppBind{}, err
	}
	return ResolvedAppBind{
		Name:        bind.Name,
		Source:      source,
		Destination: bind.Path,
		ReadOnly:    p.ReadOnly || bind.ReadOnly,
	}, nil
}

// allowlistContains reports exact membership.
func allowlistContains(names []string, candidate string) bool {
	for _, name := range names {
		if name == candidate {
			return true
		}
	}
	return false
}

// resolveSource resolves and verifies the policy source against the filesystem.
func (p AppBindPolicy) resolveSource() (string, error) {
	resolvedSource, err := filepath.EvalSymlinks(p.Source)
	if err != nil {
		return "", fmt.Errorf("%w: policy %q source %q: %v", ErrBindPolicy, p.Name, p.Source, err)
	}
	info, err := os.Lstat(resolvedSource)
	if err != nil {
		return "", fmt.Errorf("%w: policy %q source %q: %v", ErrBindPolicy, p.Name, p.Source, err)
	}
	if !bindSourceTypeAllowed(info.Mode()) {
		return "", fmt.Errorf("%w: policy %q source %q must be a regular file or directory", ErrBindPolicy, p.Name, p.Source)
	}
	if p.Root == "" {
		return resolvedSource, nil
	}
	resolvedRoot, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		return "", fmt.Errorf("%w: policy %q root %q: %v", ErrBindPolicy, p.Name, p.Root, err)
	}
	if !resolvedUnderRoot(resolvedRoot, resolvedSource) {
		return "", fmt.Errorf("%w: policy %q source %q resolves outside root %q", ErrBindPolicy, p.Name, p.Source, p.Root)
	}
	return resolvedSource, nil
}

// bindSourceTypeAllowed accepts only regular files and directories. Devices,
// sockets, FIFOs, symlinks, and other special files are refused.
func bindSourceTypeAllowed(mode os.FileMode) bool {
	return mode.IsRegular() || mode.IsDir()
}

// resolvedUnderRoot reports whether target is root itself or a descendant of
// root. Both paths must already be resolved.
func resolvedUnderRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
