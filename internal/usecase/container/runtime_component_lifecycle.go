package container

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
		return err
	}
	container, err := m.find(ctx, command)
	if err != nil {
		return err
	}
	if container != nil {
		running, runErr := m.runtime.IsContainerRunning(ctx, container.ID)
		if runErr != nil {
			return runErr
		}
		if running {
			return nil
		}
		return m.runtime.StartContainer(ctx, container.ID)
	}
	config, err := m.componentConfig(command, command.PortPublishes)
	if err != nil {
		return err
	}
	created, err := m.runtime.CreateContainer(ctx, config)
	if err != nil {
		return err
	}
	return m.runtime.StartContainer(ctx, created.ID)
}

// componentConfig produces the only component container shape that lifecycle
// commands may create. It is shared by prepare and cutover rollback so a
// failed listener transfer always restores the identical probe-only edge.
func (m *runtimeComponentLifecycleManager) componentConfig(command domain.RuntimeSelfUpdateCommand, ports []domain.ContainerPortPublish) (*domain.ContainerConfig, error) {
	env, err := componentLifecycleEnvironment(command.EnvironmentFile)
	if err != nil {
		return nil, err
	}
	config := &domain.ContainerConfig{
		Image: command.DesiredImage, Name: command.TargetComponentID, Env: env,
		Labels: componentLifecycleLabels(command), NetworkMode: command.InternalNetwork,
		PortPublishes: append([]domain.ContainerPortPublish(nil), ports...), RestartPolicy: domain.RestartPolicyAlways,
		Cmd:             []string{"serve", "--role", string(command.TargetComponentRole), "--config", "/etc/gordon/role.toml"},
		ReadOnlyVolumes: map[string]string{"/etc/gordon/role.toml": command.ConfigFile},
		Volumes:         componentPersistentVolumes(command), Aliases: []string{"gordon-" + string(command.TargetComponentRole)},
	}
	if command.TargetComponentRole == domain.ComponentRoleRuntime {
		config.Env = append(config.Env, "GORDON_COMPONENT_ID="+command.TargetComponentID)
		if source, rewritten := runtimeComponentSocketMount(config.Env); source != "" {
			config.Env = rewritten
			config.ReadOnlyVolumes["/run/gordon/runtime.sock"] = source
		}
	}
	if err := m.policy.CheckContainerConfig(command.RuntimeCommandIdentity, "", *config); err != nil {
		return nil, err
	}
	return config, nil
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
func (m *runtimeComponentLifecycleManager) health(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	container, err := m.find(ctx, command)
	if err != nil {
		return err
	}
	return m.healthContainer(ctx, container)
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
func (m *runtimeComponentLifecycleManager) connect(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if !safeComponentNetwork(command.InternalNetwork) {
		return fmt.Errorf("invalid component network")
	}
	container, err := m.find(ctx, command)
	if err != nil || container == nil {
		return err
	}
	return m.runtime.ConnectContainerToNetwork(ctx, container.Name, command.InternalNetwork)
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
	for _, port := range ports {
		if !validLoopbackBootstrapPort(port) || !allowedPreparedPort(role, port) {
			return false
		}
	}
	return true
}

func validLoopbackBootstrapPort(port domain.ContainerPortPublish) bool {
	return port.Protocol == domain.NetworkProtocolTCP && port.HostIP == "127.0.0.1" && port.HostPort >= 1 && port.HostPort <= 65535 && port.ContainerPort >= 1 && port.ContainerPort <= 65535
}

func allowedPreparedPort(role domain.ComponentRole, port domain.ContainerPortPublish) bool {
	switch role {
	case domain.ComponentRoleRuntime:
		return port.HostPort == 19444 && port.ContainerPort == 9444
	case domain.ComponentRoleControl:
		return port.HostPort == 19090 || port.HostPort == 19443
	case domain.ComponentRoleEdge:
		return port.HostPort == 18080
	default:
		return false
	}
}

func safeComponentNetwork(network string) bool {
	return strings.HasPrefix(network, "gordon-internal-") && filepath.Base(network) == network && !strings.ContainsAny(network, " /\\")
}
