package container

import (
	"context"
	"errors"
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

// MigrationCutoverCommitter durably records a completed runtime-owned edge
// handoff. Its implementation lives outside this use case so the runtime never
// imports control/app code or gains any extra socket capability.
type MigrationCutoverCommitter interface {
	// RecordMigrationCutoverSubphase durably precedes each engine mutation in
	// the listener handoff. The subphase is an allowlisted domain value, never
	// a raw engine status or error.
	RecordMigrationCutoverSubphase(context.Context, domain.RuntimeSelfUpdateCommand, domain.MigrationCutoverSubphase) error
	CommitMigrationCutover(context.Context, domain.RuntimeSelfUpdateCommand) error
}

// MigrationCutoverFailureRecorder stores a fixed operational outcome after a
// rolled-back handoff. It accepts no engine error so durable status cannot
// expose container IDs, host paths, listener ports, or secrets.
type MigrationCutoverFailureRecorder interface {
	RecordMigrationCutoverFailure(context.Context, domain.RuntimeSelfUpdateCommand, string, bool) error
}

// MigrationCutoverRecoveryState is intentionally optional for unit-only
// lifecycle implementations. The production checkpoint store implements it,
// causing every post-restart activation to inspect the managed inventory
// rather than trusting an in-memory transaction edge.
type MigrationCutoverRecoveryState interface {
	MigrationCutoverSubphase(context.Context, domain.RuntimeSelfUpdateCommand) (domain.MigrationCutoverSubphase, error)
}

type runtimeComponentLifecycleManager struct {
	runtime   out.ContainerRuntime
	policy    RuntimePolicy
	committer MigrationCutoverCommitter
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

// WithMigrationCutoverCommitter requires the runtime that owns final listener
// activation to persist its result before returning. This makes a client that
// is terminated with the old monolith recoverable through durable status.
func WithMigrationCutoverCommitter(manager RuntimeComponentLifecycleManager, committer MigrationCutoverCommitter) RuntimeComponentLifecycleManager {
	concrete, ok := manager.(*runtimeComponentLifecycleManager)
	if !ok || concrete == nil {
		return manager
	}
	concrete.committer = committer
	return concrete
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
// failed listener transfer can restore the identical probe-only edge.
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
	if err := m.mountCanonicalRegistryStorage(command, config); err != nil {
		return nil, err
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
// mountCanonicalRegistryStorage reuses the old monolith's configured registry
// directory. It is deliberately not a generation-named volume: registry blobs
// and manifests survive cutover and are writable only by the runtime-created
// registry role. The policy allowlists the exact configured host directory.
func (m *runtimeComponentLifecycleManager) mountCanonicalRegistryStorage(command domain.RuntimeSelfUpdateCommand, config *domain.ContainerConfig) error {
	if command.TargetComponentRole != domain.ComponentRoleRegistry || strings.TrimSpace(m.policy.RegistryStorageRoot) == "" {
		return nil
	}
	root := filepath.Clean(m.policy.RegistryStorageRoot)
	// The replacement runtime runs in a container and cannot stat arbitrary
	// host paths. The old monolith preflight already validates this configured
	// directory; runtime policy then permits only this exact absolute source
	// when asking the engine to bind it into the registry component.
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return fmt.Errorf("canonical registry storage is not configured")
	}
	config.Volumes = map[string]string{"/var/lib/gordon/registry": root}
	return nil
}

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
	prepared        *domain.Container
	old             *domain.Container
	appNetworkNames []string
}

// activateEdge transfers listener ownership as a compensating transaction.
// Cold cutover normally has no old container because the host service is
// already stopped. When a managed old container is present, label validation
// and the existing compensation path remain in force. Any failure restores
// and proves the prepared probe-only edge before returning the error.
const edgeCutoverTransactionTimeout = 15 * time.Second

func (m *runtimeComponentLifecycleManager) activateEdge(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if recovery, ok := m.committer.(MigrationCutoverRecoveryState); ok && recovery != nil {
		subphase, err := recovery.MigrationCutoverSubphase(ctx, command)
		if err != nil {
			return componentLifecycleError("load cutover state", err)
		}
		if subphase != domain.MigrationCutoverSubphaseNone {
			return m.reconcileInterruptedEdgeActivation(ctx, command)
		}
	}
	activation, err := m.validateEdgeActivation(ctx, command)
	if err != nil {
		return err
	}
	// Stopping the old monolith terminates the CLI/gRPC caller that supplied
	// ctx. Listener ownership is now runtime-owned, so finish its bounded
	// transaction independently rather than letting that expected disconnect
	// cancel final activation or compensation.
	transactionCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), edgeCutoverTransactionTimeout)
	defer cancel()
	return m.transferEdgeListener(transactionCtx, command, activation)
}

// reconcileInterruptedEdgeActivation observes inventory after a runtime
// restart. A prior process may have removed the probe-only edge, so recovery
// never assumes that a prepared container still exists. It either commits an
// already healthy final listener or proves the prepared rollback (and the old
// owner when one is present) before returning a retryable, sanitized outcome.
func (m *runtimeComponentLifecycleManager) reconcileInterruptedEdgeActivation(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	containers, err := m.runtime.ListContainers(ctx, true)
	if err != nil {
		return componentLifecycleError("list cutover inventory", err)
	}
	old, target, err := m.cutoverInventory(command, containers)
	if err != nil {
		return err
	}
	oldRunning := false
	if old != nil {
		oldRunning, err = m.runtime.IsContainerRunning(ctx, old.ID)
		if err != nil {
			return componentLifecycleError("inspect old serving", err)
		}
	}
	if m.completedFinalCutover(ctx, command, target, oldRunning) {
		return nil
	}

	restoreErr := m.restoreInterruptedEdgeInventory(ctx, command, target, old, oldRunning)
	retryable := restoreErr == nil
	if recordErr := m.recordCutoverFailure(ctx, command, "cutover_failed", retryable); recordErr != nil {
		return componentLifecycleError("record cutover failure", recordErr)
	}
	if !retryable {
		return componentLifecycleError("restore", restoreErr)
	}
	return componentLifecycleError("recovered", errors.New("cutover rollback completed"))
}

func (m *runtimeComponentLifecycleManager) completedFinalCutover(ctx context.Context, command domain.RuntimeSelfUpdateCommand, target *domain.Container, oldRunning bool) bool {
	if target == nil || oldRunning || !containerPortsMatch(target, command.FinalPortPublishes) || m.healthContainer(ctx, target) != nil {
		return false
	}
	if m.recordCutoverSubphase(ctx, command, domain.MigrationCutoverSubphaseBeforeCommit) != nil {
		return false
	}
	return m.committer.CommitMigrationCutover(ctx, command) == nil
}

func (m *runtimeComponentLifecycleManager) restoreInterruptedEdgeInventory(ctx context.Context, command domain.RuntimeSelfUpdateCommand, target, old *domain.Container, oldRunning bool) error {
	var restoreErr error
	if target != nil && containerPortsMatch(target, command.FinalPortPublishes) {
		restoreErr = errors.Join(restoreErr, m.runtime.StopContainer(ctx, target.ID))
		restoreErr = errors.Join(restoreErr, m.runtime.RemoveContainer(ctx, target.ID, true))
		target = nil
	}
	if target == nil {
		restoreErr = errors.Join(restoreErr, m.restorePreparedEdge(ctx, command, command.PortPublishes))
	} else if running, runErr := m.runtime.IsContainerRunning(ctx, target.ID); runErr != nil {
		restoreErr = errors.Join(restoreErr, runErr)
	} else if !running {
		restoreErr = errors.Join(restoreErr, m.runtime.StartContainer(ctx, target.ID))
	}
	if old != nil && !oldRunning {
		restoreErr = errors.Join(restoreErr, m.runtime.StartContainer(ctx, old.ID))
	}
	if restoreErr != nil {
		return restoreErr
	}
	return m.proveRollbackInventory(ctx, command)
}

func (m *runtimeComponentLifecycleManager) cutoverInventory(command domain.RuntimeSelfUpdateCommand, containers []*domain.Container) (*domain.Container, *domain.Container, error) {
	var old, target *domain.Container
	for _, container := range containers {
		if container == nil {
			continue
		}
		if container.Name == command.OldServingComponentID {
			if container.Labels[domain.LabelManaged] != "true" || container.Labels[domain.LabelComponent] == "true" {
				return nil, nil, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "old serving container is not Gordon-managed", CommandID: command.ID, ComponentID: command.OldServingComponentID, Generation: command.Generation}
			}
			old = container
		}
		if container.Name == command.TargetComponentID {
			if !isManagedLifecycleComponent(container, command) {
				return nil, nil, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "component lifecycle target labels are not Gordon-owned", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
			}
			target = container
		}
	}
	return old, target, nil
}

// proveRollbackInventory explicitly validates the public old listener and the
// restored bootstrap edge after every compensating command. Successful engine
// calls alone are not considered retry-safe.
func (m *runtimeComponentLifecycleManager) proveRollbackInventory(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	containers, err := m.runtime.ListContainers(ctx, true)
	if err != nil {
		return err
	}
	old, prepared, err := m.cutoverInventory(command, containers)
	if err != nil || prepared == nil || !containerPortsMatch(prepared, command.PortPublishes) {
		return errors.New("rollback inventory proof failed")
	}
	if old != nil {
		if !containerPortsMatch(old, command.FinalPortPublishes) {
			return errors.New("rollback inventory proof failed")
		}
		if err := m.healthContainer(ctx, old); err != nil {
			return err
		}
	}
	return m.healthContainer(ctx, prepared)
}

func containerPortsMatch(container *domain.Container, expected []domain.ContainerPortPublish) bool {
	if container == nil || len(container.Ports) != len(expected) {
		return false
	}
	actual := append([]int(nil), container.Ports...)
	slices.Sort(actual)
	wanted := make([]int, 0, len(expected))
	for _, port := range expected {
		wanted = append(wanted, port.HostPort)
	}
	slices.Sort(wanted)
	return slices.Equal(actual, wanted)
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
	if old != nil && !finalPortsMatchOld(command.FinalPortPublishes, old.Ports) {
		return edgeActivation{}, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "edge activation final ports do not match old serving container", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
	}
	appNetworkNames, err := m.preparedEdgeAppNetworks(ctx, command, prepared)
	if err != nil {
		return edgeActivation{}, err
	}
	return edgeActivation{prepared: prepared, old: old, appNetworkNames: appNetworkNames}, nil
}

// preparedEdgeAppNetworks verifies every requested attachment while the
// probe-only edge is still running. This prevents a command from using
// activation to join an arbitrary network and makes the later replacement
// retain exactly the routing connectivity which passed the prepared probes.
func (m *runtimeComponentLifecycleManager) preparedEdgeAppNetworks(ctx context.Context, command domain.RuntimeSelfUpdateCommand, prepared *domain.Container) ([]string, error) {
	if len(command.EdgeAppNetworks) == 0 {
		return nil, nil
	}
	if len(command.EdgeAppNetworks) > domain.MaxEdgeAppNetworks {
		return nil, m.deniedAppNetwork(command)
	}
	networks, err := m.runtime.ListNetworks(ctx)
	if err != nil {
		return nil, componentLifecycleError("list edge networks", err)
	}
	names := make([]string, 0, len(command.EdgeAppNetworks))
	seen := make(map[string]struct{}, len(command.EdgeAppNetworks))
	for _, name := range command.EdgeAppNetworks {
		if !safeManagedAppNetworkName(name, m.policy.ManagedNetworkPrefix) {
			return nil, m.deniedAppNetwork(command)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, m.deniedAppNetwork(command)
		}
		seen[name] = struct{}{}
		network := namedNetwork(networks, name)
		if !validManagedAppNetwork(network, name, m.policy.ManagedNetworkPrefix) || !slices.Contains(network.Containers, prepared.Name) {
			return nil, m.deniedAppNetwork(command)
		}
		names = append(names, name)
	}
	return names, nil
}

func namedNetwork(networks []*domain.NetworkInfo, name string) *domain.NetworkInfo {
	var found *domain.NetworkInfo
	for _, network := range networks {
		if network != nil && network.Name == name {
			if found != nil {
				return nil
			}
			found = network
		}
	}
	return found
}

func (m *runtimeComponentLifecycleManager) transferEdgeListener(ctx context.Context, command domain.RuntimeSelfUpdateCommand, activation edgeActivation) error {
	preparedPorts := append([]domain.ContainerPortPublish(nil), command.PortPublishes...)
	oldStopped, preparedStopped, preparedRemoved := false, false, false
	var final *domain.Container
	rollback := func() error {
		return m.rollbackEdgeActivation(ctx, command, activation, preparedPorts, final, oldStopped, preparedStopped, preparedRemoved)
	}
	failCutover := func(err error) error {
		rollbackErr := rollback()
		// A retry is safe only if every compensating command succeeded. Never
		// advertise retryability after a partial restore: the durable recovery
		// path must then inspect and repair the managed inventory first.
		if recordErr := m.recordCutoverFailure(ctx, command, cutoverFailureCode(err), rollbackErr == nil); recordErr != nil {
			return componentLifecycleError("record cutover failure", recordErr)
		}
		if rollbackErr != nil {
			return componentLifecycleError("restore", errors.Join(err, rollbackErr))
		}
		return componentLifecycleError("activate", err)
	}
	if activation.old != nil {
		if err := m.runCutoverMutation(ctx, command, domain.MigrationCutoverSubphaseBeforeOldStop, func() error { return m.runtime.StopContainer(ctx, activation.old.ID) }); err != nil {
			return componentLifecycleError("stop old serving", err)
		}
		oldStopped = true
	}
	if err := m.runCutoverMutation(ctx, command, domain.MigrationCutoverSubphaseBeforePreparedStop, func() error { return m.runtime.StopContainer(ctx, activation.prepared.ID) }); err != nil {
		return failCutover(err)
	}
	preparedStopped = true
	if err := m.runCutoverMutation(ctx, command, domain.MigrationCutoverSubphaseBeforePreparedRemove, func() error { return m.runtime.RemoveContainer(ctx, activation.prepared.ID, true) }); err != nil {
		return failCutover(err)
	}
	preparedRemoved = true
	if err := m.recordCutoverSubphase(ctx, command, domain.MigrationCutoverSubphaseBeforeFinalCreate); err != nil {
		return failCutover(err)
	}
	config, err := m.componentConfig(command, command.FinalPortPublishes)
	if err != nil {
		return failCutover(err)
	}
	final, err = m.startFinalEdgeListener(ctx, command, config)
	if err != nil {
		return failCutover(err)
	}
	if err := m.connectFinalEdgeAppNetworks(ctx, command, activation.appNetworkNames); err != nil {
		return failCutover(err)
	}
	if err := m.healthContainer(ctx, final); err != nil {
		return failCutover(err)
	}
	// The replacement runtime may continue this handler after stopping a managed
	// old monolith, whose CLI process can consequently disappear before receiving
	// a reply. Commit only once the final listener is healthy; a failed commit
	// restores the probe-only edge and, when present, the managed old owner.
	if err := m.commitFinalCutover(ctx, command); err != nil {
		return failCutover(err)
	}
	return nil
}

func (m *runtimeComponentLifecycleManager) commitFinalCutover(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	if strings.TrimSpace(m.policy.MigrationStateRoot) != "" && m.committer == nil {
		return fmt.Errorf("migration cutover durability is not configured")
	}
	if m.committer == nil {
		return nil
	}
	if err := m.recordCutoverSubphase(ctx, command, domain.MigrationCutoverSubphaseBeforeCommit); err != nil {
		return err
	}
	return m.committer.CommitMigrationCutover(ctx, command)
}

func (m *runtimeComponentLifecycleManager) runCutoverMutation(ctx context.Context, command domain.RuntimeSelfUpdateCommand, subphase domain.MigrationCutoverSubphase, mutate func() error) error {
	if err := m.recordCutoverSubphase(ctx, command, subphase); err != nil {
		return err
	}
	return mutate()
}

func (m *runtimeComponentLifecycleManager) recordCutoverSubphase(ctx context.Context, command domain.RuntimeSelfUpdateCommand, subphase domain.MigrationCutoverSubphase) error {
	if m.committer == nil {
		return nil
	}
	return m.committer.RecordMigrationCutoverSubphase(ctx, command, subphase)
}

func (m *runtimeComponentLifecycleManager) recordCutoverFailure(ctx context.Context, command domain.RuntimeSelfUpdateCommand, code string, retryable bool) error {
	recorder, ok := m.committer.(MigrationCutoverFailureRecorder)
	if !ok || recorder == nil {
		return nil
	}
	return recorder.RecordMigrationCutoverFailure(ctx, command, code, retryable)
}

func cutoverFailureCode(err error) string {
	if transientEdgeListenerReleaseError(err) {
		return "listener_release_timeout"
	}
	return "cutover_failed"
}

const (
	edgeListenerReleaseTimeout = 2 * time.Second
	edgeListenerRetryInterval  = 50 * time.Millisecond
)

// startFinalEdgeListener retries only the rootless engine's brief port-release
// window after the old listener has stopped. A failed start is removed before
// another attempt, so there is never a second serving edge and rollback still
// restores the probe-only edge and old listener on exhaustion.
func (m *runtimeComponentLifecycleManager) startFinalEdgeListener(ctx context.Context, command domain.RuntimeSelfUpdateCommand, config *domain.ContainerConfig) (*domain.Container, error) {
	deadline := time.NewTimer(edgeListenerReleaseTimeout)
	defer deadline.Stop()
	var lastErr error
	for {
		created, err := m.runtime.CreateContainer(ctx, config)
		if err == nil {
			if err = m.recordCutoverSubphase(ctx, command, domain.MigrationCutoverSubphaseBeforeFinalStart); err == nil {
				err = m.runtime.StartContainer(ctx, created.ID)
			}
			if err == nil {
				return created, nil
			}
			cleanupErr := errors.Join(m.runtime.StopContainer(ctx, created.ID), m.runtime.RemoveContainer(ctx, created.ID, true))
			if cleanupErr != nil {
				return created, errors.Join(err, cleanupErr)
			}
		}
		lastErr = err
		if !transientEdgeListenerReleaseError(err) {
			return nil, err
		}
		timer := time.NewTimer(edgeListenerRetryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			timer.Stop()
			return nil, lastErr
		case <-timer.C:
		}
	}
}

func transientEdgeListenerReleaseError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") || strings.Contains(message, "port is already allocated")
}

func (m *runtimeComponentLifecycleManager) connectFinalEdgeAppNetworks(ctx context.Context, command domain.RuntimeSelfUpdateCommand, names []string) error {
	for _, name := range names {
		if err := m.runtime.ConnectContainerToNetwork(ctx, command.TargetComponentID, name); err != nil && !alreadyConnectedNetworkError(err) {
			return componentLifecycleError("restore edge network", err)
		}
	}
	return nil
}

func (m *runtimeComponentLifecycleManager) rollbackEdgeActivation(ctx context.Context, command domain.RuntimeSelfUpdateCommand, activation edgeActivation, preparedPorts []domain.ContainerPortPublish, final *domain.Container, oldStopped, preparedStopped, preparedRemoved bool) error {
	var restoreErr error
	if final != nil {
		restoreErr = errors.Join(restoreErr, m.runtime.StopContainer(ctx, final.ID))
		restoreErr = errors.Join(restoreErr, m.runtime.RemoveContainer(ctx, final.ID, true))
	}
	if preparedRemoved {
		restoreErr = errors.Join(restoreErr, m.restorePreparedEdge(ctx, command, preparedPorts))
	} else if preparedStopped {
		restoreErr = errors.Join(restoreErr, m.runtime.StartContainer(ctx, activation.prepared.ID))
	}
	if oldStopped {
		restoreErr = errors.Join(restoreErr, m.runtime.StartContainer(ctx, activation.old.ID))
	}
	if activation.old == nil {
		restoreErr = errors.Join(restoreErr, m.proveRollbackInventory(ctx, command))
	}
	return restoreErr
}

func (m *runtimeComponentLifecycleManager) restorePreparedEdge(ctx context.Context, command domain.RuntimeSelfUpdateCommand, ports []domain.ContainerPortPublish) error {
	config, err := m.componentConfig(command, ports)
	if err != nil {
		return err
	}
	restored, err := m.runtime.CreateContainer(ctx, config)
	if err != nil {
		return err
	}
	return m.runtime.StartContainer(ctx, restored.ID)
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
	return nil, nil
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
		if port.Protocol != domain.NetworkProtocolTCP || !approvedFinalHostIP(port.HostIP) || port.HostPort < 1 || port.HostPort > 65535 || port.ContainerPort < 1 || port.ContainerPort > 65535 {
			return false
		}
		if _, exists := seen[port.HostPort]; exists {
			return false
		}
		seen[port.HostPort] = struct{}{}
	}
	return true
}

func approvedFinalHostIP(hostIP string) bool {
	return hostIP == "0.0.0.0" || hostIP == "127.0.0.1"
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
	if !domain.IsSafeEdgeAppNetworkName(name) {
		return false
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || name == "bridge" || name == "host" || name == "none" || name == "default" || safeComponentNetwork(name) || strings.HasPrefix(name, prefix+"-internal-") {
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
const (
	managedControlSecretsPath   = "/var/lib/gordon/secrets" // #nosec G101 -- fixed mount destination, not credential material.
	managedControlSecretsVolume = "gordon-control-secrets"
)

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
	volumes := map[string]string{"/var/lib/gordon": name}
	if command.TargetComponentRole == domain.ComponentRoleControl {
		// This name deliberately excludes migration and generation identifiers so
		// replacing control cannot replace its keyring or password store.
		volumes[managedControlSecretsPath] = managedControlSecretsVolume
	}
	return volumes
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
