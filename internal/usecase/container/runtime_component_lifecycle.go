package container

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bnema/gordon/internal/boundaries/out"
	"github.com/bnema/gordon/internal/domain"
)

// RuntimeComponentLifecycleManager is the runtime-local implementation of the
// restricted migration lifecycle protocol. It is deliberately defined in the
// runtime use case and wraps the socket-owning runtime adapter; no control
// package can obtain this capability.
type RuntimeComponentLifecycleManager interface {
	ApplyComponentLifecycle(context.Context, domain.RuntimeSelfUpdateCommand) error
}

type runtimeComponentLifecycleManager struct {
	runtime out.ContainerRuntime
	policy  RuntimePolicy
}

// componentLifecycleOperationError exposes only a fixed operation name to the
// remote caller; the wrapped runtime error remains local because engine errors
// can include host paths or command arguments.
type componentLifecycleOperationError struct {
	operation string
	err       error
}

func (e componentLifecycleOperationError) Error() string {
	return "component lifecycle " + e.operation + " failed"
}
func (e componentLifecycleOperationError) Unwrap() error { return e.err }

func componentLifecycleError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return componentLifecycleOperationError{operation: operation, err: err}
}

func NewRuntimeComponentLifecycleManager(runtime out.ContainerRuntime, policy RuntimePolicy) RuntimeComponentLifecycleManager {
	if runtime == nil {
		return nil
	}
	return &runtimeComponentLifecycleManager{runtime: runtime, policy: policy.normalize()}
}

func (m *runtimeComponentLifecycleManager) ApplyComponentLifecycle(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if command.LifecycleAction == "" {
		return errRuntimeSelfUpdateUnavailable
	}
	if err := m.policy.CheckSelfUpdate(command); err != nil {
		return err
	}
	if !validComponentLifecycleTarget(command) {
		return RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "component lifecycle target is not Gordon-owned", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
	}
	switch command.LifecycleAction {
	case domain.RuntimeComponentLifecycleEnsureNetwork:
		return m.ensureNetwork(ctx, command)
	case domain.RuntimeComponentLifecycleStart, domain.RuntimeComponentLifecycleReplace:
		return m.start(ctx, command)
	case domain.RuntimeComponentLifecycleStop:
		return m.stop(ctx, command)
	case domain.RuntimeComponentLifecycleHealth:
		return m.health(ctx, command)
	case domain.RuntimeComponentLifecycleLogs:
		return m.logs(ctx, command)
	case domain.RuntimeComponentLifecycleConnect:
		return m.connect(ctx, command)
	case domain.RuntimeComponentLifecycleRemove:
		return m.remove(ctx, command)
	case domain.RuntimeComponentLifecycleTransferChannel:
		// The old authority acknowledges the ordered bootstrap handoff. The
		// control client reconnects using its configured authenticated endpoint;
		// no socket capability is transferred across component boundaries.
		return nil
	case domain.RuntimeComponentLifecycleActivate:
		return m.activateEdge(ctx, command)
	case domain.RuntimeComponentLifecycleDrain:
		// Drain is a readiness transition only; listener ownership changes only
		// in the activate transaction below.
		return m.health(ctx, command)
	default:
		return fmt.Errorf("unsupported component lifecycle action")
	}
}

func (m *runtimeComponentLifecycleManager) ensureNetwork(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if !safeComponentNetwork(command.InternalNetwork) {
		return fmt.Errorf("invalid component network")
	}
	exists, err := m.runtime.NetworkExists(ctx, command.InternalNetwork)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return m.runtime.CreateNetwork(ctx, command.InternalNetwork, domain.NetworkConfig{Driver: "bridge", Internal: true, Labels: map[string]string{domain.LabelComponent: "true", domain.LabelComponentMigrationID: strings.TrimPrefix(command.PolicyDecisionID, "migration:")}})
}

func (m *runtimeComponentLifecycleManager) start(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if strings.TrimSpace(command.DesiredImage) == "" || !safeComponentNetwork(command.InternalNetwork) {
		return fmt.Errorf("invalid component desired state")
	}
	if !approvedPreparedPortPublishes(command.TargetComponentRole, command.PortPublishes) {
		return fmt.Errorf("invalid component bootstrap port binding")
	}
	if err := approvedComponentConfigFile(command.ConfigFile); err != nil {
		return componentLifecycleError("validate config", err)
	}
	component, err := m.find(ctx, command)
	if err != nil {
		return componentLifecycleError("find", err)
	}
	if component != nil {
		running, runErr := m.runtime.IsContainerRunning(ctx, component.ID)
		if runErr != nil {
			return componentLifecycleError("inspect", runErr)
		}
		if running {
			return nil
		}
		return componentLifecycleError("start", m.runtime.StartContainer(ctx, component.ID))
	}
	config, err := m.componentConfig(command, command.PortPublishes)
	if err != nil {
		return componentLifecycleError("build config", err)
	}
	created, err := m.runtime.CreateContainer(ctx, config)
	if err != nil {
		return componentLifecycleError("create", err)
	}
	return componentLifecycleError("start", m.runtime.StartContainer(ctx, created.ID))
}

// componentConfig produces the only component container shape that lifecycle
// commands may create. It is shared by prepare and cutover rollback so a
// failed listener transfer always restores the identical probe-only edge.
func (m *runtimeComponentLifecycleManager) componentConfig(command domain.RuntimeSelfUpdateCommand, ports []domain.ContainerPortPublish) (*domain.ContainerConfig, error) {
	env, err := componentLifecycleEnvironment(command.EnvironmentFile)
	if err != nil {
		return nil, err
	}
	configFile, err := componentLifecycleConfigFile(command, ports)
	if err != nil {
		return nil, err
	}
	config := &domain.ContainerConfig{
		Image: command.DesiredImage, Name: command.TargetComponentID, Env: env,
		Labels: componentLifecycleLabels(command), NetworkMode: command.InternalNetwork,
		PortPublishes: append([]domain.ContainerPortPublish(nil), ports...), RestartPolicy: domain.RestartPolicyAlways,
		Cmd:             []string{"serve", "--role", string(command.TargetComponentRole), "--config", "/etc/gordon/role.toml"},
		ReadOnlyVolumes: map[string]string{"/etc/gordon/role.toml": configFile},
		Volumes:         componentPersistentVolumes(command), Aliases: []string{"gordon-" + string(command.TargetComponentRole)},
	}
	if command.TargetComponentRole == domain.ComponentRoleRuntime {
		config.Env = append(config.Env, "GORDON_COMPONENT_ID="+command.TargetComponentID)
		if source, rewritten := runtimeComponentSocketMount(config.Env); source != "" {
			config.Env = rewritten
			config.ReadOnlyVolumes["/run/gordon/runtime.sock"] = source
		}
	}
	if err := m.mountMigrationRuntimeSocketState(command, config); err != nil {
		return nil, err
	}
	if err := m.mountMigrationComponentConfigState(command, config); err != nil {
		return nil, err
	}
	if err := m.policy.CheckContainerConfig(command.RuntimeCommandIdentity, "", *config); err != nil {
		return nil, err
	}
	return config, nil
}

// mountMigrationRuntimeSocketState gives runtime a writable private state
// directory and control a read-only view for Unix socket connect. Other roles
// receive neither. The directory is a generated immediate child of the
// configured migration root and contains no engine socket.
func (m *runtimeComponentLifecycleManager) mountMigrationRuntimeSocketState(command domain.RuntimeSelfUpdateCommand, config *domain.ContainerConfig) error {
	if command.TargetComponentRole != domain.ComponentRoleRuntime && command.TargetComponentRole != domain.ComponentRoleControl {
		return nil
	}
	if strings.TrimSpace(m.policy.MigrationStateRoot) == "" {
		// Legacy unit-only policy fixtures do not model migration state. Production
		// runtime wiring always sets this root before lifecycle is available.
		return nil
	}
	root := filepath.Clean(m.policy.MigrationStateRoot)
	if !filepath.IsAbs(root) {
		return fmt.Errorf("migration runtime socket root is not configured")
	}
	id := strings.TrimPrefix(command.PolicyDecisionID, "migration:")
	if !componentMigrationID(id) {
		return fmt.Errorf("invalid migration runtime socket identity")
	}
	source := filepath.Join(root, id)
	if err := prepareMigrationSocketStateDirectory(root, source); err != nil {
		return err
	}
	destination := filepath.Join("/var/lib/gordon/migration", id)
	if command.TargetComponentRole == domain.ComponentRoleRuntime {
		config.Volumes[destination] = source
		return nil
	}
	// Control can connect to the private Gordon runtime socket but cannot
	// modify its parent directory. It receives a separate writable child only
	// for atomically checkpointing authenticated edge attestation.
	config.ReadOnlyVolumes[destination] = source
	config.Volumes[filepath.Join(destination, "attestation")] = filepath.Join(source, "attestation")
	return nil
}

// mountMigrationComponentConfigState gives only the replacement runtime a
// read-only view of generated role manifests at their host paths. The runtime
// validates these paths before asking the engine to bind them into registry or
// edge; without this view it cannot safely continue after handoff. No other
// role receives the directory and it contains no runtime or engine socket.
func (m *runtimeComponentLifecycleManager) mountMigrationComponentConfigState(command domain.RuntimeSelfUpdateCommand, config *domain.ContainerConfig) error {
	if command.TargetComponentRole != domain.ComponentRoleRuntime || strings.TrimSpace(m.policy.MigrationStateRoot) == "" {
		return nil
	}
	root := filepath.Clean(m.policy.MigrationStateRoot)
	if !filepath.IsAbs(root) {
		return fmt.Errorf("migration component configuration root is not configured")
	}
	for _, name := range []string{"config", "env"} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("invalid migration component %s root", name)
		}
		if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- private migration component directory.
			return fmt.Errorf("restrict migration component %s root: %w", name, err)
		}
		config.ReadOnlyVolumes[path] = path
	}
	return nil
}

func prepareMigrationSocketStateDirectory(root, path string) error {
	if filepath.Dir(path) != root {
		return fmt.Errorf("invalid migration runtime socket directory")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create migration runtime socket directory: %w", err)
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("invalid migration runtime socket directory")
		}
		if current == root {
			break
		}
	}
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- private socket directory requires owner execute.
		return fmt.Errorf("restrict migration runtime socket directory: %w", err)
	}
	attestation := filepath.Join(path, "attestation")
	if err := os.MkdirAll(attestation, 0o700); err != nil {
		return fmt.Errorf("create migration attestation directory: %w", err)
	}
	info, err := os.Lstat(attestation)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("invalid migration attestation directory")
	}
	if err := os.Chmod(attestation, 0o700); err != nil { // #nosec G302 -- private migration attestation directory.
		return fmt.Errorf("restrict migration attestation directory: %w", err)
	}
	return nil
}

type edgeActivation struct {
	prepared *domain.Container
	old      *domain.Container
}

// activateEdge transfers listener ownership as a compensating transaction.
// The old process is a container selected by its Gordon labels, never the
// invoking CLI/control host process. Any failure restores both the old
// listener and the prepared probe-only edge before returning the error.
func (m *runtimeComponentLifecycleManager) activateEdge(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	activation, err := m.validateEdgeActivation(ctx, command)
	if err != nil {
		return err
	}
	return m.transferEdgeListener(ctx, command, activation)
}

func (m *runtimeComponentLifecycleManager) validateEdgeActivation(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (edgeActivation, error) {
	if command.TargetComponentRole != domain.ComponentRoleEdge || strings.TrimSpace(command.OldServingComponentID) == "" {
		return edgeActivation{}, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "edge activation requires a managed old serving container", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
	}
	if !approvedFinalPortPublishes(command.FinalPortPublishes) {
		return edgeActivation{}, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "edge activation final port bindings are not allowed", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
	}
	prepared, err := m.find(ctx, command)
	if err != nil {
		return edgeActivation{}, err
	}
	if prepared == nil {
		return edgeActivation{}, fmt.Errorf("prepared edge component not found")
	}
	if err := m.healthContainer(ctx, prepared); err != nil {
		return edgeActivation{}, fmt.Errorf("prepared edge health before listener transfer: %w", err)
	}
	old, err := m.managedOldServingContainer(ctx, command)
	if err != nil {
		return edgeActivation{}, err
	}
	if !finalPortsMatchOld(command.FinalPortPublishes, old.Ports) {
		return edgeActivation{}, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "edge activation final ports do not match old serving container", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
	}
	return edgeActivation{prepared: prepared, old: old}, nil
}

func (m *runtimeComponentLifecycleManager) transferEdgeListener(ctx context.Context, command domain.RuntimeSelfUpdateCommand, activation edgeActivation) error {
	preparedPorts := append([]domain.ContainerPortPublish(nil), command.PortPublishes...)
	oldStopped, preparedStopped, preparedRemoved := false, false, false
	var final *domain.Container
	rollback := func() {
		m.rollbackEdgeActivation(ctx, command, activation, preparedPorts, final, oldStopped, preparedStopped, preparedRemoved)
	}
	if err := m.runtime.StopContainer(ctx, activation.old.ID); err != nil {
		return fmt.Errorf("stop old serving container: %w", err)
	}
	oldStopped = true
	if err := m.runtime.StopContainer(ctx, activation.prepared.ID); err != nil {
		rollback()
		return fmt.Errorf("stop prepared edge: %w", err)
	}
	preparedStopped = true
	if err := m.runtime.RemoveContainer(ctx, activation.prepared.ID, true); err != nil {
		rollback()
		return fmt.Errorf("remove prepared edge: %w", err)
	}
	preparedRemoved = true
	config, err := m.componentConfig(command, command.FinalPortPublishes)
	if err != nil {
		rollback()
		return err
	}
	final, err = m.runtime.CreateContainer(ctx, config)
	if err != nil {
		rollback()
		return fmt.Errorf("create final edge: %w", err)
	}
	if err := m.runtime.StartContainer(ctx, final.ID); err != nil {
		rollback()
		return fmt.Errorf("start final edge: %w", err)
	}
	if err := m.healthContainer(ctx, final); err != nil {
		rollback()
		return fmt.Errorf("postcheck final edge: %w", err)
	}
	return nil
}

func (m *runtimeComponentLifecycleManager) rollbackEdgeActivation(ctx context.Context, command domain.RuntimeSelfUpdateCommand, activation edgeActivation, preparedPorts []domain.ContainerPortPublish, final *domain.Container, oldStopped, preparedStopped, preparedRemoved bool) {
	if final != nil {
		_ = m.runtime.StopContainer(ctx, final.ID)
		_ = m.runtime.RemoveContainer(ctx, final.ID, true)
	}
	if preparedRemoved {
		m.restorePreparedEdge(ctx, command, preparedPorts)
	} else if preparedStopped {
		_ = m.runtime.StartContainer(ctx, activation.prepared.ID)
	}
	if oldStopped {
		_ = m.runtime.StartContainer(ctx, activation.old.ID)
	}
}

func (m *runtimeComponentLifecycleManager) restorePreparedEdge(ctx context.Context, command domain.RuntimeSelfUpdateCommand, ports []domain.ContainerPortPublish) {
	config, err := m.componentConfig(command, ports)
	if err != nil {
		return
	}
	restored, err := m.runtime.CreateContainer(ctx, config)
	if err == nil {
		_ = m.runtime.StartContainer(ctx, restored.ID)
	}
}

func (m *runtimeComponentLifecycleManager) managedOldServingContainer(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (*domain.Container, error) {
	containers, err := m.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, container := range containers {
		if container != nil && container.Name == command.OldServingComponentID {
			if container.Labels[domain.LabelManaged] != "true" || container.Labels[domain.LabelComponent] == "true" {
				return nil, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "old serving container is not Gordon-managed", CommandID: command.ID, ComponentID: command.OldServingComponentID, Generation: command.Generation}
			}
			return container, nil
		}
	}
	return nil, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "old serving container is not available", CommandID: command.ID, ComponentID: command.OldServingComponentID, Generation: command.Generation}
}

func (m *runtimeComponentLifecycleManager) healthContainer(ctx context.Context, container *domain.Container) error {
	if container == nil {
		return fmt.Errorf("component not found")
	}
	running, err := m.runtime.IsContainerRunning(ctx, container.ID)
	if err != nil || !running {
		return fmt.Errorf("component is not running")
	}
	status, hasCheck, err := m.runtime.GetContainerHealthStatus(ctx, container.ID)
	if err != nil {
		return err
	}
	if hasCheck && !strings.EqualFold(status, "healthy") {
		return fmt.Errorf("component is unhealthy")
	}
	return nil
}

func approvedFinalPortPublishes(ports []domain.ContainerPortPublish) bool {
	if len(ports) == 0 {
		return false
	}
	seen := make(map[int]struct{}, len(ports))
	for _, port := range ports {
		if port.Protocol != domain.NetworkProtocolTCP || port.HostIP != "0.0.0.0" || port.HostPort < 1 || port.HostPort > 65535 || port.ContainerPort < 1 || port.ContainerPort > 65535 {
			return false
		}
		if _, exists := seen[port.HostPort]; exists {
			return false
		}
		seen[port.HostPort] = struct{}{}
	}
	return true
}

func finalPortsMatchOld(final []domain.ContainerPortPublish, old []int) bool {
	if len(final) != len(old) {
		return false
	}
	allowed := make(map[int]struct{}, len(old))
	for _, port := range old {
		allowed[port] = struct{}{}
	}
	if len(allowed) != len(old) {
		return false
	}
	for _, port := range final {
		if _, exists := allowed[port.HostPort]; !exists {
			return false
		}
	}
	return true
}

func (m *runtimeComponentLifecycleManager) stop(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	container, err := m.find(ctx, command)
	if err != nil || container == nil {
		return err
	}
	return m.runtime.StopContainer(ctx, container.ID)
}

const (
	componentHealthStartupTimeout = 2 * time.Second
	componentHealthRetryInterval  = 25 * time.Millisecond
)

// health tolerates the short eventual-consistency window between a rootless
// Podman start acknowledgement and its container-list API. It remains bounded
// by the command context and never treats a missing container as healthy.
func (m *runtimeComponentLifecycleManager) health(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	deadline := time.NewTimer(componentHealthStartupTimeout)
	defer deadline.Stop()
	var lastErr error
	for {
		container, err := m.find(ctx, command)
		if err == nil {
			err = m.healthContainer(ctx, container)
		}
		if err == nil {
			return nil
		}
		lastErr = err
		timer := time.NewTimer(componentHealthRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return componentLifecycleError("health", ctx.Err())
		case <-deadline.C:
			timer.Stop()
			return componentLifecycleError("health", lastErr)
		case <-timer.C:
		}
	}
}
func (m *runtimeComponentLifecycleManager) logs(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	container, err := m.find(ctx, command)
	if err != nil || container == nil {
		return err
	}
	logs, err := m.runtime.GetContainerLogs(ctx, container.ID, false)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(logs, 64<<10))
	return logs.Close()
}

// connect gives a prepared edge a single, verified attachment to a pre-existing
// Gordon application network. It intentionally does not reuse the internal
// component-network path: no command can use this action to gain arbitrary
// network authority.
func (m *runtimeComponentLifecycleManager) connect(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if command.TargetComponentRole != domain.ComponentRoleEdge || !safeManagedAppNetworkName(command.InternalNetwork, m.policy.ManagedNetworkPrefix) {
		return m.deniedAppNetwork(command)
	}
	container, err := m.find(ctx, command)
	if err != nil {
		return err
	}
	if container == nil {
		return fmt.Errorf("prepared edge component not found")
	}
	network, err := m.managedAppNetwork(ctx, command.InternalNetwork)
	if err != nil {
		return err
	}
	if slices.Contains(network.Containers, container.Name) {
		return nil
	}
	if err := m.runtime.ConnectContainerToNetwork(ctx, container.Name, network.Name); err != nil && !alreadyConnectedNetworkError(err) {
		return err
	}
	return nil
}

func (m *runtimeComponentLifecycleManager) managedAppNetwork(ctx context.Context, name string) (*domain.NetworkInfo, error) {
	networks, err := m.runtime.ListNetworks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list managed app networks: %w", err)
	}
	var found *domain.NetworkInfo
	for _, network := range networks {
		if network == nil || network.Name != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("managed app network is ambiguous")
		}
		found = network
	}
	if !validManagedAppNetwork(found, name, m.policy.ManagedNetworkPrefix) {
		return nil, fmt.Errorf("invalid managed app network")
	}
	return found, nil
}

func validManagedAppNetwork(network *domain.NetworkInfo, name, prefix string) bool {
	if network == nil || network.Name != name || network.Internal || network.Labels[domain.LabelManaged] != "true" || !safeManagedAppNetworkName(name, prefix) {
		return false
	}
	return slices.ContainsFunc(network.Containers, safeGordonTargetAlias)
}

func safeManagedAppNetworkName(name, prefix string) bool {
	if name != strings.TrimSpace(name) {
		return false
	}
	name, prefix = strings.TrimSpace(name), strings.TrimSpace(prefix)
	if name == "" || prefix == "" || filepath.Base(name) != name || strings.ContainsAny(name, " /\\\\") {
		return false
	}
	if name == "bridge" || name == "host" || name == "none" || name == "default" || safeComponentNetwork(name) || strings.HasPrefix(name, prefix+"-internal-") {
		return false
	}
	return strings.HasPrefix(name, prefix+"-")
}

func safeGordonTargetAlias(alias string) bool {
	if alias != strings.TrimSpace(alias) {
		return false
	}
	alias = strings.TrimSpace(alias)
	return strings.HasPrefix(alias, "gordon-target-") && len(strings.TrimPrefix(alias, "gordon-target-")) != 0 && filepath.Base(alias) == alias && !strings.ContainsAny(alias, " /\\\\")
}

func alreadyConnectedNetworkError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already connected") || strings.Contains(message, "already exists")
}

func (m *runtimeComponentLifecycleManager) deniedAppNetwork(command domain.RuntimeSelfUpdateCommand) error {
	return RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedNetworkDenied, Message: "edge app network is not Gordon-managed", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
}
func (m *runtimeComponentLifecycleManager) remove(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if !command.PreserveVolumes {
		return fmt.Errorf("component lifecycle cleanup must preserve volumes")
	}
	container, err := m.find(ctx, command)
	if err != nil || container == nil {
		return err
	}
	return m.runtime.RemoveContainer(ctx, container.ID, true)
}
func (m *runtimeComponentLifecycleManager) find(ctx context.Context, command domain.RuntimeSelfUpdateCommand) (*domain.Container, error) {
	containers, err := m.runtime.ListContainers(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, container := range containers {
		if container != nil && container.Name == command.TargetComponentID {
			if !isManagedLifecycleComponent(container, command) {
				return nil, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "component lifecycle target labels are not Gordon-owned", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
			}
			return container, nil
		}
	}
	return nil, nil
}

func validComponentLifecycleTarget(command domain.RuntimeSelfUpdateCommand) bool {
	generation := "-g" + strconv.FormatUint(command.Generation, 10)
	migrationID := strings.TrimPrefix(command.PolicyDecisionID, "migration:")
	if migrationID == "" || command.Generation == 0 {
		return false
	}
	if command.LifecycleAction == domain.RuntimeComponentLifecycleEnsureNetwork {
		prefix := "gordon-network-"
		return command.TargetComponentRole == domain.ComponentRoleRuntime && strings.HasPrefix(command.TargetComponentID, prefix) && strings.TrimSuffix(strings.TrimPrefix(command.TargetComponentID, prefix), generation) == migrationID && strings.HasSuffix(command.TargetComponentID, generation)
	}
	if (command.LifecycleAction == domain.RuntimeComponentLifecycleActivate || command.LifecycleAction == domain.RuntimeComponentLifecycleDrain) && command.TargetComponentRole != domain.ComponentRoleEdge {
		return false
	}
	prefix := "gordon-" + string(command.TargetComponentRole) + "-"
	return strings.HasPrefix(command.TargetComponentID, prefix) && strings.TrimSuffix(strings.TrimPrefix(command.TargetComponentID, prefix), generation) == migrationID && strings.HasSuffix(command.TargetComponentID, generation)
}

func componentLifecycleLabels(command domain.RuntimeSelfUpdateCommand) map[string]string {
	return map[string]string{
		domain.LabelComponent:                 "true",
		domain.LabelComponentRole:             string(command.TargetComponentRole),
		domain.LabelComponentVersion:          command.TargetVersion,
		domain.LabelComponentGeneration:       strconv.FormatUint(command.Generation, 10),
		domain.LabelComponentMigrationID:      strings.TrimPrefix(command.PolicyDecisionID, "migration:"),
		domain.LabelComponentOwner:            "runtime",
		domain.LabelComponentDesiredStateHash: command.DesiredStateHash,
	}
}

func isManagedLifecycleComponent(container *domain.Container, command domain.RuntimeSelfUpdateCommand) bool {
	if container == nil || container.Labels == nil || container.Labels[domain.LabelComponent] != "true" || container.Labels[domain.LabelComponentRole] != string(command.TargetComponentRole) {
		return false
	}
	if container.Labels[domain.LabelComponentGeneration] != strconv.FormatUint(command.Generation, 10) || container.Labels[domain.LabelComponentMigrationID] != strings.TrimPrefix(command.PolicyDecisionID, "migration:") {
		return false
	}
	return container.Labels[domain.LabelComponentOwner] == "runtime" || container.Labels[domain.LabelComponentOwner] == "migration"
}

// componentLifecycleEnvironment reads only a runtime-owned generated env file.
// It returns generic errors and never includes values in failures or logs.
func componentPersistentVolumes(command domain.RuntimeSelfUpdateCommand) map[string]string {
	// Persistent state belongs to explicit named volumes. Edge is stateless;
	// registry storage is distinct so it can never be removed with a component.
	name := "gordon-" + string(command.TargetComponentRole) + "-" + strings.TrimPrefix(command.PolicyDecisionID, "migration:") + "-g" + strconv.FormatUint(command.Generation, 10)
	if command.TargetComponentRole == domain.ComponentRoleEdge {
		return nil
	}
	if command.TargetComponentRole == domain.ComponentRoleRegistry {
		name = "gordon-registry-" + strings.TrimPrefix(command.PolicyDecisionID, "migration:") + "-g" + strconv.FormatUint(command.Generation, 10)
	}
	return map[string]string{"/var/lib/gordon": name}
}

// componentLifecycleConfigFile permits the dedicated final edge manifest only
// for an activation's validated public bindings. All prepare and rollback paths
// retain edge.toml, preserving the authenticated probe configuration.
func componentLifecycleConfigFile(command domain.RuntimeSelfUpdateCommand, ports []domain.ContainerPortPublish) (string, error) {
	path := command.ConfigFile
	if command.TargetComponentRole == domain.ComponentRoleEdge && command.LifecycleAction == domain.RuntimeComponentLifecycleActivate && approvedFinalPortPublishes(ports) && filepath.Base(path) == "edge.toml" {
		path = filepath.Join(filepath.Dir(path), "edge-final.toml")
	}
	if err := approvedComponentConfigFile(path); err != nil {
		return "", err
	}
	return path, nil
}

func approvedComponentConfigFile(path string) error {
	clean := filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(clean) || !strings.Contains(clean, "/migration/config/") {
		return fmt.Errorf("invalid component configuration file")
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("invalid component configuration file")
	}
	return nil
}

func componentLifecycleEnvironment(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("invalid component environment file")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 64<<10 {
		return nil, fmt.Errorf("invalid component environment file")
	}
	values := make([]string, 0)
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") && strings.Contains(line, "=") {
			values = append(values, line)
		}
	}
	return values, nil
}
func runtimeComponentSocketMount(environment []string) (string, []string) {
	copyOf := append([]string(nil), environment...)
	for index, entry := range copyOf {
		key, value, found := strings.Cut(entry, "=")
		if !found || (key != "CONTAINER_HOST" && key != "DOCKER_HOST" && key != "PODMAN_HOST") {
			continue
		}
		path := strings.TrimPrefix(strings.TrimSpace(value), "unix://")
		if !isRuntimeSocketMount(path) {
			continue
		}
		copyOf[index] = key + "=unix:///run/gordon/runtime.sock"
		return path, copyOf
	}
	return "", copyOf
}

func approvedPreparedPortPublishes(role domain.ComponentRole, ports []domain.ContainerPortPublish) bool {
	// The prepared edge alone may expose one temporary probe listener. It must
	// be literal loopback TCP and cannot reuse its in-container serving port;
	// every other role remains host-TCP-free until the activate transaction.
	if len(ports) == 0 {
		return role != domain.ComponentRoleEdge
	}
	if role != domain.ComponentRoleEdge || len(ports) != 1 {
		return false
	}
	port := ports[0]
	return port.Protocol == domain.NetworkProtocolTCP && port.HostPort >= 1 && port.HostPort <= 65535 && port.ContainerPort >= 1 && port.ContainerPort <= 65535 && port.HostPort != port.ContainerPort && port.HostIP == "127.0.0.1" && net.ParseIP(port.HostIP).IsLoopback()
}

func safeComponentNetwork(network string) bool {
	return strings.HasPrefix(network, "gordon-internal-") && filepath.Base(network) == network && !strings.ContainsAny(network, " /\\")
}
