package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ComponentLauncher is the migration-only lifecycle boundary. Its production
// implementation speaks the authenticated runtime command API; control never
// receives a Podman/Docker adapter or a socket through this interface.
// RuntimeCommandChannelTransfer models the bootstrap handoff after the old
// runtime authority has started the new runtime. Implementations must use the
// authenticated runtime command path; it is never a socket handoff to control.
type RuntimeCommandChannelTransfer interface {
	TransferRuntimeCommandChannel(context.Context, ComponentLaunchComponent) error
}

// RuntimeHandoffClient is the authenticated, migration-safe surface required
// before control can trust a replacement runtime. It intentionally contains no
// engine adapter or socket capability.
type RuntimeHandoffClient interface {
	out.RuntimeSelfUpdater
	out.RuntimeEnvironmentProbe
	out.RuntimeStateSubscriber
	out.RuntimeHealthClient
}

// RuntimeHandoffDialer creates an authenticated client for the deterministic
// bootstrap endpoint of the prepared runtime. Implementations must not fall
// back to the old runtime endpoint on an error.
type RuntimeHandoffDialer func(context.Context, ComponentLaunchComponent) (RuntimeHandoffClient, error)

type ComponentLauncher interface {
	CreateInternalNetwork(context.Context, ComponentLaunchPlan) error
	StartComponent(context.Context, ComponentLaunchComponent) error
	StopComponent(context.Context, ComponentLaunchComponent) error
	CheckComponentHealth(context.Context, ComponentLaunchComponent) error
	ConnectEdgeToAppNetwork(context.Context, ComponentLaunchComponent, string) error
	RemovePreparedComponent(context.Context, ComponentLaunchComponent) error
}

// ComponentLaunchPlan contains the deterministic, non-public component
// generation prepared beside the still-serving monolith.
type ComponentLaunchPlan struct {
	MigrationID     string
	Generation      uint64
	Version         string
	Image           string
	InternalNetwork string
	Components      []ComponentLaunchComponent
	AppNetworks     []string
	// PublicCutover is deliberately always false during prepare. Traffic
	// switching has a separately gated phase and is not a launcher operation.
	PublicCutover bool
}

type ComponentLaunchComponent struct {
	Role             domain.ComponentRole
	ComponentID      string
	Image            string
	InternalNetwork  string
	EnvironmentFile  string
	ConfigFile       string
	Labels           map[string]string
	DesiredStateHash string
	// PortPublishes contains only checkpointed deterministic bindings. During
	// prepare these are loopback bootstrap/probe bindings; final public
	// bindings are supplied only by the separately authorized activate action.
	PortPublishes     []domain.ContainerPortPublish
	BootstrapEndpoint string
}

func (p ComponentLaunchPlan) Roles() []domain.ComponentRole {
	roles := make([]domain.ComponentRole, 0, len(p.Components))
	for _, component := range p.Components {
		roles = append(roles, component.Role)
	}
	return roles
}

// NewComponentLaunchPlan produces stable names/labels so a retry can discover
// exactly the same generation. It intentionally creates no runtime object.
func NewComponentLaunchPlan(checkpoint MigrationCheckpoint) (ComponentLaunchPlan, error) {
	if !componentLabelValue.MatchString(checkpoint.MigrationID) || checkpoint.ComponentGeneration == 0 {
		return ComponentLaunchPlan{}, fmt.Errorf("migration ID and component generation are required")
	}
	version := strings.TrimSpace(checkpoint.TargetVersion)
	if version == "" {
		version = "prepared"
	}
	image := strings.TrimSpace(checkpoint.TargetImage)
	if image == "" {
		return ComponentLaunchPlan{}, fmt.Errorf("target image is required")
	}
	plan := ComponentLaunchPlan{MigrationID: checkpoint.MigrationID, Generation: checkpoint.ComponentGeneration, Version: version, Image: image, InternalNetwork: fmt.Sprintf("gordon-internal-%s-g%d", checkpoint.MigrationID, checkpoint.ComponentGeneration), AppNetworks: safeAppNetworks(checkpoint.EdgeAppNetworks)}
	envByRole := componentEnvReferences(checkpoint.EnvFileReferences)
	configByRole := componentConfigReferences(checkpoint.ConfigFileReferences)
	if err := validateComponentLaunchReferences(checkpoint, envByRole, configByRole); err != nil {
		return ComponentLaunchPlan{}, err
	}
	if checkpoint.BootstrapRuntimeEndpoint != "" && !validBootstrapRuntimeEndpoint(checkpoint.BootstrapRuntimeEndpoint, nil) {
		return ComponentLaunchPlan{}, fmt.Errorf("invalid runtime bootstrap transport")
	}
	if len(checkpoint.PreparedPortBindings) != 0 && !validPreparedEdgeProbeBindings(checkpoint.BootstrapEdgeProbeEndpoint, checkpoint.PreparedPortBindings) {
		return ComponentLaunchPlan{}, fmt.Errorf("invalid edge bootstrap port binding")
	}
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry, domain.ComponentRoleEdge} {
		componentID := fmt.Sprintf("gordon-%s-%s-g%d", role, checkpoint.MigrationID, checkpoint.ComponentGeneration)
		identity, ok := domain.FixedComponentProcessIdentity(role)
		if !ok {
			return ComponentLaunchPlan{}, fmt.Errorf("component process identity is required")
		}
		hash := componentRoleLaunchHash(componentID, image, plan.InternalNetwork, identity)
		labels, err := BuildComponentLabels(ComponentLabelRequest{Role: role, Version: version, Generation: checkpoint.ComponentGeneration, MigrationID: checkpoint.MigrationID, Owner: "migration", DesiredStateHash: hash})
		if err != nil {
			return ComponentLaunchPlan{}, err
		}
		component := ComponentLaunchComponent{Role: role, ComponentID: componentID, Image: image, InternalNetwork: plan.InternalNetwork, EnvironmentFile: envByRole[role], ConfigFile: configByRole[role], Labels: labels, DesiredStateHash: hash}
		if role == domain.ComponentRoleRuntime {
			component.BootstrapEndpoint = checkpoint.BootstrapRuntimeEndpoint
		}
		if role == domain.ComponentRoleEdge {
			component.PortPublishes = componentPreparedPorts(checkpoint.PreparedPortBindings, role)
		}
		plan.Components = append(plan.Components, component)
	}
	return plan, nil
}

func safeAppNetworks(networks []string) []string {
	unique := make(map[string]struct{}, len(networks))
	for _, network := range networks {
		network = strings.TrimSpace(network)
		if network != "" && filepath.Base(network) == network && !strings.ContainsAny(network, " /\\\\") {
			unique[network] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for network := range unique {
		result = append(result, network)
	}
	sort.Strings(result)
	return result
}

func componentLaunchHash(componentID, image, network string) string {
	sum := sha256.Sum256([]byte(componentID + "\x00" + image + "\x00" + network))
	return hex.EncodeToString(sum[:])
}

func componentRoleLaunchHash(componentID, image, network string, identity domain.ComponentProcessIdentity) string {
	sum := sha256.Sum256([]byte(componentID + "\x00" + image + "\x00" + network + "\x00" + identity.User + "\x00" + domain.ContainerVolumeOptionChown))
	return hex.EncodeToString(sum[:])
}

func validateComponentLaunchReferences(checkpoint MigrationCheckpoint, envByRole, configByRole map[domain.ComponentRole]string) error {
	if len(checkpoint.ConfigFileReferences) != 0 {
		if len(checkpoint.ConfigFileReferences) != len(componentRoles) || len(configByRole) != len(componentRoles) {
			return fmt.Errorf("component configuration references must cover every role")
		}
		for _, role := range componentRoles {
			if !approvedLaunchReference(configByRole[role], "config", checkpoint, string(role)+".toml") {
				return fmt.Errorf("invalid %s component configuration reference", role)
			}
		}
	}
	if len(checkpoint.EnvFileReferences) != 0 {
		if len(envByRole) != len(checkpoint.EnvFileReferences) {
			return fmt.Errorf("component environment references must have distinct roles")
		}
		for role, path := range envByRole {
			if !approvedLaunchReference(path, "env", checkpoint, string(role)+".env") {
				return fmt.Errorf("invalid %s component environment reference", role)
			}
		}
	}
	return nil
}

func approvedLaunchReference(path, kind string, checkpoint MigrationCheckpoint, name string) bool {
	clean := filepath.Clean(strings.TrimSpace(path))
	generation := filepath.Dir(clean)
	migration := filepath.Dir(generation)
	kindDir := filepath.Dir(migration)
	return filepath.IsAbs(clean) && filepath.Base(clean) == name && filepath.Base(generation) == fmt.Sprint(checkpoint.ComponentGeneration) &&
		filepath.Base(migration) == checkpoint.MigrationID && filepath.Base(kindDir) == kind && filepath.Base(filepath.Dir(kindDir)) == "migration"
}

func componentPreparedPorts(bindings []MigrationPortBinding, role domain.ComponentRole) []domain.ContainerPortPublish {
	ports := make([]domain.ContainerPortPublish, 0)
	for _, binding := range bindings {
		if binding.Role == string(role) {
			ports = append(ports, domain.ContainerPortPublish{HostIP: binding.HostIP, HostPort: binding.HostPort, ContainerPort: binding.ContainerPort, Protocol: domain.NetworkProtocol(binding.Protocol)})
		}
	}
	return ports
}

func componentPublicPorts(bindings []MigrationPortBinding, role domain.ComponentRole) []domain.ContainerPortPublish {
	return componentPreparedPorts(bindings, role)
}

func componentEnvReferences(references []string) map[domain.ComponentRole]string {
	byRole := make(map[domain.ComponentRole]string)
	for _, reference := range references {
		role := domain.ComponentRole(strings.TrimSuffix(filepath.Base(reference), ".env"))
		if domain.IsKnownComponentRole(role) {
			byRole[role] = reference
		}
	}
	return byRole
}

// RuntimeComponentLauncher serializes lifecycle intents into authenticated
// RuntimeSelfUpdate commands. It has no container runtime field by design.
type RuntimeComponentLauncher struct {
	mu      sync.RWMutex
	runtime out.RuntimeSelfUpdater
	handoff RuntimeHandoffDialer
	now     func() time.Time
}

func NewRuntimeComponentLauncher(runtime out.RuntimeSelfUpdater) (*RuntimeComponentLauncher, error) {
	return NewRuntimeComponentLauncherWithHandoff(runtime, nil)
}

// NewRuntimeComponentLauncherWithHandoff makes runtime authority swappable
// only after a live, authenticated replacement has proved its identity and
// actual-state stream. Until then all commands continue to use oldRuntime.
func NewRuntimeComponentLauncherWithHandoff(oldRuntime out.RuntimeSelfUpdater, handoff RuntimeHandoffDialer) (*RuntimeComponentLauncher, error) {
	if oldRuntime == nil {
		return nil, fmt.Errorf("runtime self-update client is required")
	}
	return &RuntimeComponentLauncher{runtime: oldRuntime, handoff: handoff, now: time.Now}, nil
}

func (l *RuntimeComponentLauncher) CreateInternalNetwork(ctx context.Context, plan ComponentLaunchPlan) error {
	componentID := fmt.Sprintf("gordon-network-%s-g%d", plan.MigrationID, plan.Generation)
	return l.send(ctx, domain.RuntimeComponentLifecycleEnsureNetwork, ComponentLaunchComponent{Role: domain.ComponentRoleRuntime, ComponentID: componentID, InternalNetwork: plan.InternalNetwork, DesiredStateHash: componentLaunchHash(plan.InternalNetwork, plan.Image, plan.MigrationID), Labels: map[string]string{domain.LabelComponentVersion: plan.Version, domain.LabelComponentGeneration: fmt.Sprintf("%d", plan.Generation), domain.LabelComponentMigrationID: plan.MigrationID}}, plan.Generation, plan.MigrationID, "network")
}
func (l *RuntimeComponentLauncher) StartComponent(ctx context.Context, component ComponentLaunchComponent) error {
	return l.send(ctx, domain.RuntimeComponentLifecycleStart, component, componentGeneration(component), componentMigrationID(component), "start")
}
func (l *RuntimeComponentLauncher) StopComponent(ctx context.Context, component ComponentLaunchComponent) error {
	return l.send(ctx, domain.RuntimeComponentLifecycleStop, component, componentGeneration(component), componentMigrationID(component), "stop")
}

// SelfUpdateRuntime forwards a cutover command to the currently proven runtime
// authority. The launcher swaps that authority only after the replacement
// runtime's authenticated health and actual-state proof, so a self-hosted CLI
// never asks the monolith it will stop to own the cutover transaction.
func (l *RuntimeComponentLauncher) SelfUpdateRuntime(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (domain.RuntimeCommandResult, error) {
	if l == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime component launcher is not configured")
	}
	l.mu.RLock()
	runtime := l.runtime
	l.mu.RUnlock()
	if runtime == nil {
		return domain.RuntimeCommandResult{}, fmt.Errorf("runtime self-update client is required")
	}
	return runtime.SelfUpdateRuntime(ctx, command)
}
func (l *RuntimeComponentLauncher) TransferRuntimeCommandChannel(ctx context.Context, component ComponentLaunchComponent) error {
	if component.Role != domain.ComponentRoleRuntime {
		return fmt.Errorf("runtime command channel can only transfer to runtime")
	}
	if l == nil || l.handoff == nil {
		return fmt.Errorf("runtime handoff target is not configured")
	}
	// This acknowledgement is deliberately sent through the old authority. It
	// authorizes bootstrap but does not claim the handoff succeeded.
	if err := l.send(ctx, domain.RuntimeComponentLifecycleTransferChannel, component, componentGeneration(component), componentMigrationID(component), "transfer-channel"); err != nil {
		return err
	}
	target, err := l.handoff(ctx, component)
	if err != nil {
		return fmt.Errorf("connect authenticated replacement runtime: %w", err)
	}
	if err := proveRuntimeHandoff(ctx, target, component); err != nil {
		return fmt.Errorf("prove replacement runtime: %w", err)
	}
	l.mu.Lock()
	l.runtime = target
	l.mu.Unlock()
	return nil
}

const (
	runtimeHandoffStartupTimeout = 10 * time.Second
	runtimeHandoffRetryInterval  = 50 * time.Millisecond
)

// proveRuntimeHandoff allows a just-started runtime a short, bounded window to
// bind its private bootstrap listener. A lifecycle Start acknowledgement means
// only that Podman started the process; it does not prove gRPC is accepting yet.
func proveRuntimeHandoff(ctx context.Context, target RuntimeHandoffClient, component ComponentLaunchComponent) error {
	if target == nil {
		return fmt.Errorf("replacement runtime client is required")
	}
	startupCtx, cancel := context.WithTimeout(ctx, runtimeHandoffStartupTimeout)
	defer cancel()
	for {
		err := proveRuntimeHandoffOnce(startupCtx, target, component)
		if err == nil || !isTransientRuntimeHandoffError(err) {
			return err
		}
		timer := time.NewTimer(runtimeHandoffRetryInterval)
		select {
		case <-startupCtx.Done():
			timer.Stop()
			return fmt.Errorf("replacement runtime did not become ready: %w", err)
		case <-timer.C:
		}
	}
}

func proveRuntimeHandoffOnce(ctx context.Context, target RuntimeHandoffClient, component ComponentLaunchComponent) error {
	probe, err := target.ProbeRuntimeEnvironment(ctx)
	if err != nil {
		return fmt.Errorf("probe replacement runtime environment: %w", err)
	}
	if !probe.APIReachable || !probe.Rootless {
		return fmt.Errorf("replacement runtime environment is not rootless and reachable")
	}
	if err := target.PingRuntime(ctx); err != nil {
		return fmt.Errorf("check replacement runtime health: %w", err)
	}
	updates, err := target.SubscribeRuntimeState(ctx)
	if err != nil {
		return fmt.Errorf("subscribe replacement runtime state: %w", err)
	}
	select {
	case snapshot, ok := <-updates:
		if !ok {
			return fmt.Errorf("replacement runtime state stream closed")
		}
		return verifyRuntimeHandoffSnapshot(component, snapshot)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isTransientRuntimeHandoffError(err error) bool {
	if err == nil {
		return false
	}
	// A gRPC status is the authoritative signal: a replacement runtime that has
	// not yet bound its bootstrap listener reports codes.Unavailable. Any other
	// status code is a real failure and must not be retried, even if its message
	// happens to contain a fallback keyword.
	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.Unavailable
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "connection refused") || strings.Contains(text, "connection error") || strings.Contains(text, "unavailable")
}

func verifyRuntimeHandoffSnapshot(component ComponentLaunchComponent, snapshot domain.RuntimeActualStateSnapshot) error {
	if snapshot.SourceComponentID != component.ComponentID {
		return fmt.Errorf("replacement runtime state source does not match component")
	}
	generation := componentGeneration(component)
	for _, container := range snapshot.Containers {
		// RuntimeContainerState.Generation is the monotonically increasing
		// snapshot sequence, not the immutable component generation. Bind the
		// proof to the signed component label instead.
		if container.Name == component.ComponentID && container.Status == domain.ContainerStatusRunning && container.Labels[domain.LabelComponent] == "true" && container.Labels[domain.LabelComponentRole] == string(domain.ComponentRoleRuntime) && container.Labels[domain.LabelComponentGeneration] == fmt.Sprintf("%d", generation) {
			return nil
		}
	}
	return fmt.Errorf("replacement runtime component generation is not live: expected=%s generation=%d observed=%s", component.ComponentID, generation, runtimeHandoffContainerSummary(snapshot.Containers))
}

func runtimeHandoffContainerSummary(containers []domain.RuntimeContainerState) string {
	states := make([]string, 0, len(containers))
	for _, container := range containers {
		states = append(states, fmt.Sprintf("%s:%s:g%d:%s", container.Name, container.Status, container.Generation, container.Labels[domain.LabelComponentRole]))
	}
	return strings.Join(states, ",")
}
func (l *RuntimeComponentLauncher) CheckComponentHealth(ctx context.Context, component ComponentLaunchComponent) error {
	return l.send(ctx, domain.RuntimeComponentLifecycleHealth, component, componentGeneration(component), componentMigrationID(component), "health")
}
func (l *RuntimeComponentLauncher) ConnectEdgeToAppNetwork(ctx context.Context, component ComponentLaunchComponent, network string) error {
	component.InternalNetwork = network
	return l.send(ctx, domain.RuntimeComponentLifecycleConnect, component, componentGeneration(component), componentMigrationID(component), "connect")
}
func (l *RuntimeComponentLauncher) RemovePreparedComponent(ctx context.Context, component ComponentLaunchComponent) error {
	return l.send(ctx, domain.RuntimeComponentLifecycleRemove, component, componentGeneration(component), componentMigrationID(component), "remove")
}

func (l *RuntimeComponentLauncher) send(ctx context.Context, action domain.RuntimeComponentLifecycleAction, component ComponentLaunchComponent, generation uint64, migrationID, operation string) error {
	if generation == 0 || strings.TrimSpace(migrationID) == "" {
		return fmt.Errorf("component lifecycle identity is required")
	}
	l.mu.RLock()
	runtime := l.runtime
	l.mu.RUnlock()
	if runtime == nil {
		return fmt.Errorf("runtime self-update client is required")
	}
	result, err := runtime.SelfUpdateRuntime(ctx, domain.RuntimeSelfUpdateCommand{
		RuntimeCommandIdentity: domain.RuntimeCommandIdentity{ID: domain.RuntimeCommandID("migration:" + migrationID + ":" + operation + ":" + component.ComponentID), IdempotencyKey: "migration:" + migrationID + ":" + operation + ":" + component.ComponentID, Generation: generation, SourceComponentID: "gordon-control", RequestedAt: l.now().UTC()},
		TargetComponentID:      component.ComponentID, TargetComponentRole: component.Role, TargetVersion: component.Labels[domain.LabelComponentVersion], Policy: domain.RuntimeSelfUpdatePolicyManualApproval, PolicyDecisionID: "migration:" + migrationID,
		LifecycleAction: action, DesiredImage: component.Image, DesiredStateHash: component.DesiredStateHash, InternalNetwork: component.InternalNetwork, EnvironmentFile: component.EnvironmentFile, ConfigFile: component.ConfigFile, PortPublishes: append([]domain.ContainerPortPublish(nil), component.PortPublishes...), PreserveVolumes: true,
	})
	if err != nil {
		return fmt.Errorf("send component %s command: %w", operation, err)
	}
	if result.Status != domain.RuntimeCommandStatusSucceeded {
		if result.Error != nil && strings.TrimSpace(result.Error.Message) != "" {
			return fmt.Errorf("component %s command was not accepted: %s", operation, result.Error.Message)
		}
		return fmt.Errorf("component %s command was not accepted", operation)
	}
	return nil
}

func componentGeneration(component ComponentLaunchComponent) uint64 {
	generation, _ := parseComponentGeneration(component.Labels)
	return generation
}
func componentMigrationID(component ComponentLaunchComponent) string {
	return component.Labels[domain.LabelComponentMigrationID]
}
func parseComponentGeneration(labels map[string]string) (uint64, error) {
	var generation uint64
	_, err := fmt.Sscan(labels[domain.LabelComponentGeneration], &generation)
	return generation, err
}

// StablePreparedComponents is used by checkpoint persistence and keeps status
// output deterministic without exposing credentials or container internals.
func StablePreparedComponents(plan ComponentLaunchPlan) []string {
	components := make([]string, 0, len(plan.Components))
	for _, component := range plan.Components {
		components = append(components, component.ComponentID)
	}
	sort.Strings(components)
	return components
}
