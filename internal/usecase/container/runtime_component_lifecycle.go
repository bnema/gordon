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
	case domain.RuntimeComponentLifecycleActivate, domain.RuntimeComponentLifecycleDrain:
		// Readiness/drain transitions are constrained to an already managed edge
		// component. The runtime never receives an arbitrary listener address.
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
	env, err := componentLifecycleEnvironment(command.EnvironmentFile)
	if err != nil {
		return err
	}
	labels := componentLifecycleLabels(command)
	config := &domain.ContainerConfig{Image: command.DesiredImage, Name: command.TargetComponentID, Env: env, Labels: labels, NetworkMode: command.InternalNetwork, RestartPolicy: domain.RestartPolicyAlways}
	if command.ConfigFile != "" {
		if err := approvedComponentConfigFile(command.ConfigFile); err != nil {
			return err
		}
		config.Cmd = []string{"serve", "--role", string(command.TargetComponentRole), "--config", "/etc/gordon/role.yaml"}
		config.ReadOnlyVolumes = map[string]string{"/etc/gordon/role.yaml": command.ConfigFile}
		config.Volumes = componentPersistentVolumes(command)
		config.Aliases = []string{"gordon-" + string(command.TargetComponentRole)}
	}
	if err := m.policy.CheckContainerConfig(command.RuntimeCommandIdentity, "", *config); err != nil {
		return err
	}
	created, err := m.runtime.CreateContainer(ctx, config)
	if err != nil {
		return err
	}
	return m.runtime.StartContainer(ctx, created.ID)
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
func safeComponentNetwork(network string) bool {
	return strings.HasPrefix(network, "gordon-internal-") && filepath.Base(network) == network && !strings.ContainsAny(network, " /\\")
}
