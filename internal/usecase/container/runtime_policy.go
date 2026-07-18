package container

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bnema/gordon/internal/domain"
)

// RuntimePolicyMode controls whether policy findings are observed or enforced.
type RuntimePolicyMode string

const (
	RuntimePolicyModeObserve RuntimePolicyMode = "observe"
	RuntimePolicyModeEnforce RuntimePolicyMode = "enforce"
)

const (
	RuntimePolicyReasonPrivilegedDenied       = "privileged_container_denied"
	RuntimePolicyReasonSocketMountDenied      = "rootless_socket_mount_denied"
	RuntimePolicyReasonUnsafeHostBindDenied   = "unsafe_host_bind_denied"
	RuntimePolicyReasonUnmanagedNetworkDenied = "unmanaged_network_denied"
	RuntimePolicyReasonCapabilityDenied       = "capability_add_denied"
	RuntimePolicyReasonUnmanagedMutation      = "unmanaged_mutation_denied"
	RuntimePolicyReasonImageRegistryDenied    = "image_registry_denied"
	RuntimePolicyReasonDigestRequired         = "image_digest_required"
)

var ErrRuntimePolicyDenied = errors.New("runtime policy denied")

// RuntimePolicyDeniedError is safe to transport or log across component boundaries.
type RuntimePolicyDeniedError struct {
	Reason      string
	Message     string
	CommandID   domain.RuntimeCommandID
	RouteDomain string
	ComponentID string
	Generation  uint64
}

func (e RuntimePolicyDeniedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

func (e RuntimePolicyDeniedError) Unwrap() error { return ErrRuntimePolicyDenied }

// RuntimePolicy configures runtime-side safety checks close to runtime authority.
type RuntimePolicy struct {
	Mode                   RuntimePolicyMode
	ManagedNetworkPrefix   string
	AllowedImageRegistries []string
	RequireImageDigest     bool
	AllowedCapAdd          []string
	RuntimeComponentID     string
	RuntimeComponentRole   domain.ComponentRole
}

func NewRuntimePolicy(mode RuntimePolicyMode) RuntimePolicy {
	return RuntimePolicy{Mode: mode, ManagedNetworkPrefix: "gordon"}
}

func (p RuntimePolicy) normalize() RuntimePolicy {
	if p.Mode == "" {
		p.Mode = RuntimePolicyModeObserve
	}
	if p.ManagedNetworkPrefix == "" {
		p.ManagedNetworkPrefix = "gordon"
	}
	return p
}

func (p RuntimePolicy) Enforced() bool { return p.normalize().Mode == RuntimePolicyModeEnforce }

func (p RuntimePolicy) CheckDeployRoute(command domain.DeployRouteCommand) error {
	p = p.normalize()
	if err := p.checkImage(command.Image, command.RuntimeCommandIdentity, command.Domain); err != nil {
		return err
	}
	return nil
}

func (p RuntimePolicy) CheckRestartRoute(command domain.RestartRouteCommand) error {
	return p.checkManagedRouteMutation(command.RuntimeCommandIdentity, command.Domain)
}

func (p RuntimePolicy) CheckRemoveRoute(command domain.RemoveRouteCommand) error {
	return p.checkManagedRouteMutation(command.RuntimeCommandIdentity, command.Domain)
}

func (p RuntimePolicy) CheckReconcile(command domain.ReconcileRuntimeCommand) error {
	for _, route := range command.DesiredRoutes {
		if err := p.checkImage(route.Image, command.RuntimeCommandIdentity, route.Domain); err != nil {
			return err
		}
	}
	return nil
}

func (p RuntimePolicy) CheckSelfUpdate(command domain.RuntimeSelfUpdateCommand) error {
	p = p.normalize()
	if strings.TrimSpace(command.TargetComponentID) == "" || !domain.IsKnownComponentRole(command.TargetComponentRole) {
		return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "only labeled Gordon component self-updates are allowed")
	}
	// Migration lifecycle commands are explicitly allowlisted, authenticated
	// control intents for Gordon-owned component names. They are executed only
	// by gordon-runtime and cannot carry raw engine flags or a socket path.
	if command.LifecycleAction != "" {
		if !validComponentLifecycleTarget(command) {
			return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "component lifecycle target is not Gordon-owned")
		}
		if strings.TrimSpace(command.DesiredImage) != "" {
			if err := p.checkImage(command.DesiredImage, command.RuntimeCommandIdentity, ""); err != nil {
				return err
			}
		}
		return nil
	}
	if strings.TrimSpace(p.RuntimeComponentID) != "" && command.TargetComponentID != p.RuntimeComponentID {
		return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "self-update target component is not authorized")
	}
	if p.RuntimeComponentRole != "" && command.TargetComponentRole != p.RuntimeComponentRole {
		return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "self-update target role is not authorized")
	}
	return nil
}

func (p RuntimePolicy) CheckContainerConfig(identity domain.RuntimeCommandIdentity, routeDomain string, cfg domain.ContainerConfig) error {
	p = p.normalize()
	if cfg.Privileged {
		return p.denied(identity, routeDomain, RuntimePolicyReasonPrivilegedDenied, "privileged containers are not allowed")
	}
	if cfg.NetworkMode != "" && !strings.HasPrefix(cfg.NetworkMode, p.ManagedNetworkPrefix+"-") && cfg.NetworkMode != p.ManagedNetworkPrefix {
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnmanagedNetworkDenied, "container network mode is outside managed networks")
	}
	for _, cap := range cfg.CapAdd {
		if !slices.Contains(p.AllowedCapAdd, cap) {
			return p.denied(identity, routeDomain, RuntimePolicyReasonCapabilityDenied, "container capability add is not allowed")
		}
	}
	for _, source := range cfg.Volumes {
		if isRuntimeSocketMount(source) {
			return p.denied(identity, routeDomain, RuntimePolicyReasonSocketMountDenied, "runtime socket mounts are not allowed")
		}
		if isUnsafeHostBind(source) {
			return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "unsafe host bind mount is not allowed")
		}
	}
	for _, source := range cfg.ReadOnlyVolumes {
		if isRuntimeSocketMount(source) {
			if isApprovedRuntimeSocketBind(cfg, source) {
				continue
			}
			return p.denied(identity, routeDomain, RuntimePolicyReasonSocketMountDenied, "runtime socket mounts are not allowed")
		}
		// Gordon role manifests are the sole host-file exception. They are
		// generated with restrictive permissions under migration/config and may
		// only be mounted read-only by a labeled component container.
		if isApprovedComponentConfigBind(cfg, source) {
			continue
		}
		if isUnsafeHostBind(source) {
			return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "unsafe host bind mount is not allowed")
		}
	}
	return nil
}

func (p RuntimePolicy) checkManagedRouteMutation(identity domain.RuntimeCommandIdentity, routeDomain string) error {
	if !domain.IsValidRouteDomain(routeDomain) {
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnmanagedMutation, "runtime command must target a managed route")
	}
	return nil
}

func (p RuntimePolicy) checkImage(image string, identity domain.RuntimeCommandIdentity, routeDomain string) error {
	p = p.normalize()
	trimmed := strings.TrimSpace(image)
	if p.RequireImageDigest && !strings.Contains(trimmed, "@sha256:") {
		return p.denied(identity, routeDomain, RuntimePolicyReasonDigestRequired, "image digest is required")
	}
	if len(p.AllowedImageRegistries) == 0 {
		return nil
	}
	registry := runtimePolicyImageRegistry(trimmed)
	if slices.Contains(p.AllowedImageRegistries, registry) {
		return nil
	}
	return p.denied(identity, routeDomain, RuntimePolicyReasonImageRegistryDenied, "image registry is not allowed")
}

func (p RuntimePolicy) denied(identity domain.RuntimeCommandIdentity, routeDomain, reason, message string) error {
	return RuntimePolicyDeniedError{Reason: reason, Message: message, CommandID: identity.ID, RouteDomain: routeDomain, ComponentID: identity.SourceComponentID, Generation: identity.Generation}
}

func runtimePolicyImageRegistry(image string) string {
	first, _, _ := strings.Cut(image, "/")
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return first
	}
	return "docker.io"
}

func isRuntimeSocketMount(source string) bool {
	clean := filepath.Clean(strings.TrimSpace(source))
	return strings.HasSuffix(clean, ".sock") && (strings.Contains(clean, "/podman/") || strings.Contains(clean, "podman.sock") || strings.Contains(clean, "docker.sock"))
}

func isApprovedRuntimeSocketBind(cfg domain.ContainerConfig, source string) bool {
	if cfg.Labels[domain.LabelComponent] != "true" || cfg.Labels[domain.LabelComponentRole] != string(domain.ComponentRoleRuntime) || !strings.HasPrefix(cfg.Name, "gordon-runtime-") || cfg.ReadOnlyVolumes["/run/gordon/runtime.sock"] != source {
		return false
	}
	// A lifecycle-generated runtime container may receive exactly the engine
	// socket source discovered from its own scoped env file. The destination is
	// fixed by the lifecycle manager; no other role reaches this exception.
	return isRuntimeSocketMount(source)
}

func isApprovedComponentConfigBind(cfg domain.ContainerConfig, source string) bool {
	if cfg.Labels[domain.LabelComponent] != "true" {
		return false
	}
	clean := filepath.Clean(strings.TrimSpace(source))
	return filepath.IsAbs(clean) && strings.Contains(clean, "/migration/config/")
}

func isUnsafeHostBind(source string) bool {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return false
	}
	if _, err := url.ParseRequestURI(trimmed); err == nil && strings.Contains(trimmed, "://") {
		return true
	}
	return !strings.HasPrefix(filepath.Clean(trimmed), "/var/lib/gordon/")
}

func RuntimePolicyDeniedEventFromError(err error, decisionID string) (domain.RuntimePolicyDeniedEvent, bool) {
	var denied RuntimePolicyDeniedError
	if !errors.As(err, &denied) {
		return domain.RuntimePolicyDeniedEvent{}, false
	}
	return domain.RuntimePolicyDeniedEvent{CommandID: denied.CommandID, RouteDomain: denied.RouteDomain, Service: denied.RouteDomain, Generation: denied.Generation, SourceComponentID: denied.ComponentID, PolicyDecisionID: decisionID, Reason: denied.Reason, Message: sanitizeRuntimeErrorMessage(denied)}, true
}

func formatPolicyReason(reason string) string {
	if reason == "" {
		return "runtime_policy_denied"
	}
	return fmt.Sprintf("runtime_policy_denied:%s", reason)
}
