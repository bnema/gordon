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
	ReadComponentLogs(context.Context, ComponentLaunchComponent) (string, error)
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
	for _, role := range []domain.ComponentRole{domain.ComponentRoleControl, domain.ComponentRoleRuntime, domain.ComponentRoleRegistry, domain.ComponentRoleEdge} {
		componentID := fmt.Sprintf("gordon-%s-%s-g%d", role, checkpoint.MigrationID, checkpoint.ComponentGeneration)
		hash := componentLaunchHash(componentID, image, plan.InternalNetwork)
		labels, err := BuildComponentLabels(ComponentLabelRequest{Role: role, Version: version, Generation: checkpoint.ComponentGeneration, MigrationID: checkpoint.MigrationID, Owner: "migration", DesiredStateHash: hash})
		if err != nil {
			return ComponentLaunchPlan{}, err
		}
		component := ComponentLaunchComponent{Role: role, ComponentID: componentID, Image: image, InternalNetwork: plan.InternalNetwork, EnvironmentFile: envByRole[role], ConfigFile: configByRole[role], Labels: labels, DesiredStateHash: hash, PortPublishes: componentPreparedPorts(checkpoint.PreparedPortBindings, role)}
		if role == domain.ComponentRoleRuntime {
			component.BootstrapEndpoint = checkpoint.BootstrapRuntimeEndpoint
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

func proveRuntimeHandoff(ctx context.Context, target RuntimeHandoffClient, component ComponentLaunchComponent) error {
	if target == nil {
		return fmt.Errorf("replacement runtime client is required")
	}
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

func verifyRuntimeHandoffSnapshot(component ComponentLaunchComponent, snapshot domain.RuntimeActualStateSnapshot) error {
	if snapshot.SourceComponentID != component.ComponentID {
		return fmt.Errorf("replacement runtime state source does not match component")
	}
	generation := componentGeneration(component)
	for _, container := range snapshot.Containers {
		if container.Name == component.ComponentID && container.Status == domain.ContainerStatusRunning && container.Generation == generation && container.Labels[domain.LabelComponent] == "true" && container.Labels[domain.LabelComponentRole] == string(domain.ComponentRoleRuntime) && container.Labels[domain.LabelComponentGeneration] == fmt.Sprintf("%d", generation) {
			return nil
		}
	}
	return fmt.Errorf("replacement runtime component generation is not live")
}
func (l *RuntimeComponentLauncher) CheckComponentHealth(ctx context.Context, component ComponentLaunchComponent) error {
	return l.send(ctx, domain.RuntimeComponentLifecycleHealth, component, componentGeneration(component), componentMigrationID(component), "health")
}
func (l *RuntimeComponentLauncher) ReadComponentLogs(ctx context.Context, component ComponentLaunchComponent) (string, error) {
	err := l.send(ctx, domain.RuntimeComponentLifecycleLogs, component, componentGeneration(component), componentMigrationID(component), "logs")
	return "", err
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
