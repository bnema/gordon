package container

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
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
	// MigrationStateRoot is the host data-dir migration root. Only generated
	// Gordon component lifecycle commands may bind one immediate child of it.
	MigrationStateRoot string
	// RegistryStorageRoot is the canonical existing registry blob store. The
	// replacement registry may bind only this exact directory, preserving blobs
	// and manifests instead of creating a generation-local empty volume.
	RegistryStorageRoot string
	// ManagedControlSecretsVolume is the installation-scoped named volume that
	// only an authenticated, runtime-generated control lifecycle config may mount.
	ManagedControlSecretsVolume string
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
		return p.checkComponentLifecycle(command)
	}
	if strings.TrimSpace(p.RuntimeComponentID) != "" && command.TargetComponentID != p.RuntimeComponentID {
		return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "self-update target component is not authorized")
	}
	if p.RuntimeComponentRole != "" && command.TargetComponentRole != p.RuntimeComponentRole {
		return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "self-update target role is not authorized")
	}
	return nil
}

func (p RuntimePolicy) checkComponentLifecycle(command domain.RuntimeSelfUpdateCommand) error {
	if !validComponentLifecycleTarget(command) {
		return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "component lifecycle target is not Gordon-owned")
	}
	requirement, ok := domain.RuntimeComponentLifecycleRequirement(command.LifecycleAction)
	if !ok {
		return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "component lifecycle action is not allowed")
	}
	switch requirement.ProfileMode {
	case domain.RuntimeComponentLifecycleProfileNone:
		if !command.LifecycleProfile.IsEmpty() {
			return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "component lifecycle profile is not allowed")
		}
	case domain.RuntimeComponentLifecycleProfileIdentityOnly:
		if !command.LifecycleProfile.IsFixedIdentityOnlyFor(command.TargetComponentRole) || !command.HasOnlyReadLifecycleIdentity() {
			return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "component lifecycle read identity is not allowed")
		}
		return nil
	case domain.RuntimeComponentLifecycleProfileFull:
		if !validRuntimeComponentLifecycleProfile(command.TargetComponentRole, command.LifecycleProfile) {
			return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "component lifecycle process identity is not allowed")
		}
	default:
		return p.denied(command.RuntimeCommandIdentity, "", RuntimePolicyReasonUnmanagedMutation, "component lifecycle profile mode is not allowed")
	}
	if strings.TrimSpace(command.DesiredImage) == "" {
		return nil
	}
	return p.checkImage(command.DesiredImage, command.RuntimeCommandIdentity, "")
}

func validRuntimeComponentLifecycleProfile(role domain.ComponentRole, actual domain.RuntimeComponentLifecycleProfile) bool {
	return actual.IsFixedFor(role)
}

func (p RuntimePolicy) CheckContainerConfig(identity domain.RuntimeCommandIdentity, routeDomain string, cfg domain.ContainerConfig) error {
	p = p.normalize()
	if err := p.checkComponentProcessIdentity(identity, routeDomain, cfg); err != nil {
		return err
	}
	if err := p.checkContainerVolumeOptions(identity, routeDomain, cfg); err != nil {
		return err
	}
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
	return p.checkContainerMounts(identity, routeDomain, cfg)
}

func (p RuntimePolicy) checkComponentProcessIdentity(identity domain.RuntimeCommandIdentity, routeDomain string, cfg domain.ContainerConfig) error {
	if cfg.Labels[domain.LabelComponent] != "true" || cfg.Labels[domain.LabelComponentDesiredStateHash] == "" {
		return nil
	}
	role := domain.ComponentRole(cfg.Labels[domain.LabelComponentRole])
	expected, ok := domain.FixedRuntimeComponentLifecycleProfile(role)
	if !ok || cfg.User != expected.ProcessIdentity.User || cfg.UsernsMode != expected.UsernsMode ||
		!slices.Equal(cfg.CapDrop, expected.CapDrop) || len(cfg.CapAdd) != 0 || cfg.NoNewPrivileges == nil || *cfg.NoNewPrivileges != expected.NoNewPrivileges {
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnmanagedMutation, "component process identity is not allowed")
	}
	return nil
}

func (p RuntimePolicy) checkContainerVolumeOptions(identity domain.RuntimeCommandIdentity, routeDomain string, cfg domain.ContainerConfig) error {
	if cfg.Labels[domain.LabelComponent] != "true" || cfg.Labels[domain.LabelComponentDesiredStateHash] == "" {
		if len(cfg.VolumeOptions) == 0 {
			return nil
		}
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "container volume options are not allowed")
	}
	role := domain.ComponentRole(cfg.Labels[domain.LabelComponentRole])
	if role == domain.ComponentRoleEdge {
		if len(cfg.VolumeOptions) == 0 {
			return nil
		}
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "component volume options are not allowed")
	}

	expectedName, ok := expectedComponentGenerationVolume(cfg, role)
	actualName, mounted := cfg.Volumes["/var/lib/gordon"]
	if role == domain.ComponentRoleRegistry && !mounted {
		if len(cfg.VolumeOptions) == 0 {
			return nil
		}
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "registry storage volume options are not allowed")
	}
	if !ok || !mounted || actualName != expectedName || len(cfg.VolumeOptions) != 1 || !domain.IsContainerVolumeChownOptions(cfg.VolumeOptions["/var/lib/gordon"]) {
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "component generation volume options are not allowed")
	}
	return nil
}

func expectedComponentGenerationVolume(cfg domain.ContainerConfig, role domain.ComponentRole) (string, bool) {
	if role != domain.ComponentRoleRuntime && role != domain.ComponentRoleControl && role != domain.ComponentRoleRegistry {
		return "", false
	}
	migrationID := cfg.Labels[domain.LabelComponentMigrationID]
	generation := cfg.Labels[domain.LabelComponentGeneration]
	if !componentMigrationID(migrationID) {
		return "", false
	}
	parsedGeneration, err := strconv.ParseUint(generation, 10, 64)
	if err != nil || parsedGeneration == 0 {
		return "", false
	}
	name := componentGenerationVolumeName(role, migrationID, parsedGeneration)
	return name, cfg.Name == name
}

func (p RuntimePolicy) checkContainerMounts(identity domain.RuntimeCommandIdentity, routeDomain string, cfg domain.ContainerConfig) error {
	if err := p.checkManagedControlSecretsMount(identity, routeDomain, cfg); err != nil {
		return err
	}
	if err := p.checkWritableContainerMounts(identity, routeDomain, cfg); err != nil {
		return err
	}
	return p.checkReadOnlyContainerMounts(identity, routeDomain, cfg)
}

func (p RuntimePolicy) checkWritableContainerMounts(identity domain.RuntimeCommandIdentity, routeDomain string, cfg domain.ContainerConfig) error {
	for destination, source := range cfg.Volumes {
		if destination == managedControlSecretsPath || source == p.ManagedControlSecretsVolume {
			continue
		}
		if !isApprovedMigrationRuntimeStateBind(p, cfg, source, false) && !isApprovedRegistryStorageBind(p, cfg, source) && isUnsafeHostBind(source) {
			return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "unsafe host bind mount is not allowed")
		}
	}
	return nil
}

func (p RuntimePolicy) checkReadOnlyContainerMounts(identity domain.RuntimeCommandIdentity, routeDomain string, cfg domain.ContainerConfig) error {
	for destination, source := range cfg.ReadOnlyVolumes {
		if destination == "/run/gordon/runtime.sock" {
			if !isApprovedRuntimeSocketBind(cfg, source) {
				return p.denied(identity, routeDomain, RuntimePolicyReasonSocketMountDenied, "runtime socket mounts are not allowed")
			}
			continue
		}
		if !isApprovedComponentConfigBind(p, cfg, source) && !isApprovedMigrationRuntimeStateBind(p, cfg, source, true) && !isApprovedMigrationEnvironmentBind(p, cfg, source) && isUnsafeHostBind(source) {
			return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "unsafe host bind mount is not allowed")
		}
	}
	return nil
}

func (p RuntimePolicy) checkManagedControlSecretsMount(identity domain.RuntimeCommandIdentity, routeDomain string, cfg domain.ContainerConfig) error {
	reserved := strings.TrimSpace(p.ManagedControlSecretsVolume)
	controlLifecycle := cfg.Labels[domain.LabelComponent] == "true" && cfg.Labels[domain.LabelComponentRole] == string(domain.ComponentRoleControl) &&
		cfg.Labels[domain.LabelComponentOwner] == "runtime" && strings.HasPrefix(cfg.Name, "gordon-control-")
	if controlLifecycle && !validManagedControlSecretsVolume(reserved) {
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "managed control secret volume is not configured")
	}
	destinationUsed, managedSourceUsed := managedControlSecretsMountUsage(cfg)
	if reserved != "" && !validManagedControlSecretsVolume(reserved) {
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "managed control secret volume is invalid")
	}
	if !destinationUsed && !managedSourceUsed {
		return nil
	}
	if !validManagedControlSecretsVolume(reserved) || !authorizedManagedControlSecretsMount(identity, cfg, reserved) {
		return p.denied(identity, routeDomain, RuntimePolicyReasonUnsafeHostBindDenied, "managed control secret volume is reserved")
	}
	return nil
}

func validManagedControlSecretsVolume(value string) bool {
	const prefix = "gordon-control-secrets-"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+16 {
		return false
	}
	for _, char := range strings.TrimPrefix(value, prefix) {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func managedControlSecretsMountUsage(cfg domain.ContainerConfig) (bool, bool) {
	_, destinationUsed := cfg.Volumes[managedControlSecretsPath]
	managedSourceUsed := false
	for _, mountedSource := range cfg.Volumes {
		managedSourceUsed = managedSourceUsed || validManagedControlSecretsVolume(mountedSource)
	}
	for destination, mountedSource := range cfg.ReadOnlyVolumes {
		destinationUsed = destinationUsed || destination == managedControlSecretsPath
		managedSourceUsed = managedSourceUsed || validManagedControlSecretsVolume(mountedSource)
	}
	return destinationUsed, managedSourceUsed
}

func authorizedManagedControlSecretsMount(identity domain.RuntimeCommandIdentity, cfg domain.ContainerConfig, reserved string) bool {
	if cfg.Volumes[managedControlSecretsPath] != reserved || cfg.Labels[domain.LabelComponent] != "true" ||
		cfg.Labels[domain.LabelComponentRole] != string(domain.ComponentRoleControl) || cfg.Labels[domain.LabelComponentOwner] != "runtime" ||
		!strings.HasPrefix(cfg.Name, "gordon-control-") || identity.SourceComponentID != "gordon-control" {
		return false
	}
	for destination, source := range cfg.Volumes {
		if validManagedControlSecretsVolume(source) && (source != reserved || destination != managedControlSecretsPath) {
			return false
		}
	}
	for destination, source := range cfg.ReadOnlyVolumes {
		if validManagedControlSecretsVolume(source) || destination == managedControlSecretsPath {
			return false
		}
	}
	return true
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

func isApprovedRuntimeSocketBind(cfg domain.ContainerConfig, source string) bool {
	if cfg.Labels[domain.LabelComponent] != "true" || cfg.Labels[domain.LabelComponentRole] != string(domain.ComponentRoleRuntime) || !strings.HasPrefix(cfg.Name, "gordon-runtime-") || cfg.ReadOnlyVolumes["/run/gordon/runtime.sock"] != source {
		return false
	}
	// A lifecycle-generated runtime container may receive exactly the engine
	// socket source selected by the active adapter. The destination is fixed by
	// the lifecycle manager; no filename convention is part of this capability.
	return isApprovedEngineSocketSource(source)
}

func isApprovedEngineSocketSource(source string) bool {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" || trimmed != source || !filepath.IsAbs(trimmed) {
		return false
	}
	clean := filepath.Clean(trimmed)
	if clean == string(filepath.Separator) || clean != trimmed {
		return false
	}
	switch filepath.Base(clean) {
	case "docker.sock":
		return isApprovedDockerEngineSocket(filepath.Dir(clean))
	case "podman.sock":
		return isApprovedPodmanEngineSocket(filepath.Dir(clean))
	default:
		return false
	}
}

func isApprovedDockerEngineSocket(dir string) bool {
	return dir == "/var/run" || dir == "/run"
}

func isApprovedPodmanEngineSocket(dir string) bool {
	if dir == "/run/podman" {
		return true
	}
	parts := strings.Split(dir, string(filepath.Separator))
	if len(parts) != 5 || parts[1] != "run" || parts[2] != "user" || parts[4] != "podman" {
		return false
	}
	for _, character := range parts[3] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return parts[3] != ""
}

func isApprovedMigrationRuntimeStateBind(policy RuntimePolicy, cfg domain.ContainerConfig, source string, readOnly bool) bool {
	if !isMigrationStateComponent(cfg) || policy.MigrationStateRoot == "" || !filepath.IsAbs(source) {
		return false
	}
	root := filepath.Clean(policy.MigrationStateRoot)
	clean := filepath.Clean(source)
	state := clean
	if filepath.Base(clean) == "attestation" {
		state = filepath.Dir(clean)
	}
	if filepath.Dir(state) != root || filepath.Base(state) == "." {
		return false
	}
	id := filepath.Base(state)
	if !componentMigrationID(id) {
		return false
	}
	destination := filepath.Join("/var/lib/gordon/migration", id)
	if cfg.Labels[domain.LabelComponentRole] == string(domain.ComponentRoleRuntime) {
		return !readOnly && clean == state && cfg.Volumes[destination] == source
	}
	if readOnly {
		return clean == state && cfg.ReadOnlyVolumes[destination] == source
	}
	// Control may write only its checkpoint attestation child. The runtime
	// socket parent remains read-only, so this mount cannot grant socket
	// deletion or replacement authority.
	return clean == filepath.Join(state, "attestation") && cfg.Volumes[filepath.Join(destination, "attestation")] == source
}

func isApprovedRegistryStorageBind(policy RuntimePolicy, cfg domain.ContainerConfig, source string) bool {
	root := filepath.Clean(strings.TrimSpace(policy.RegistryStorageRoot))
	return root != "." && filepath.IsAbs(root) && cfg.Labels[domain.LabelComponent] == "true" &&
		cfg.Labels[domain.LabelComponentRole] == string(domain.ComponentRoleRegistry) &&
		filepath.Clean(source) == root && cfg.Volumes["/var/lib/gordon/registry"] == source
}

func isMigrationStateComponent(cfg domain.ContainerConfig) bool {
	role := cfg.Labels[domain.LabelComponentRole]
	return cfg.Labels[domain.LabelComponent] == "true" && (role == string(domain.ComponentRoleRuntime) || role == string(domain.ComponentRoleControl))
}

func componentMigrationID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func isApprovedMigrationEnvironmentBind(policy RuntimePolicy, cfg domain.ContainerConfig, source string) bool {
	if cfg.Labels[domain.LabelComponent] != "true" || cfg.Labels[domain.LabelComponentRole] != string(domain.ComponentRoleRuntime) || strings.TrimSpace(policy.MigrationStateRoot) == "" {
		return false
	}
	root := filepath.Clean(policy.MigrationStateRoot)
	expected := filepath.Join(root, "env")
	return filepath.IsAbs(source) && filepath.Clean(source) == expected && cfg.ReadOnlyVolumes[source] == source
}

func isApprovedComponentConfigBind(policy RuntimePolicy, cfg domain.ContainerConfig, source string) bool {
	if cfg.Labels[domain.LabelComponent] != "true" {
		return false
	}
	clean := filepath.Clean(strings.TrimSpace(source))
	if !filepath.IsAbs(clean) {
		return false
	}
	if strings.TrimSpace(policy.MigrationStateRoot) == "" {
		return strings.Contains(clean, "/migration/config/") || strings.HasSuffix(clean, "/migration/config")
	}
	root := filepath.Join(filepath.Clean(policy.MigrationStateRoot), "config")
	relative, err := filepath.Rel(root, clean)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	if relative == "." {
		return cfg.Labels[domain.LabelComponentRole] == string(domain.ComponentRoleRuntime) && cfg.ReadOnlyVolumes[clean] == source
	}
	return true
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
