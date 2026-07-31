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

	"golang.org/x/sys/unix"

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
	if _, ok := domain.RuntimeComponentLifecycleRequirement(command.LifecycleAction); !ok {
		return domain.ErrUnsupportedComponentLifecycleAction
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
		return domain.ErrUnsupportedComponentLifecycleAction
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
	if err := approvedComponentConfigFile(command, command.ConfigFile, m.policy.MigrationStateRoot); err != nil {
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
	env, err := componentLifecycleEnvironment(command, command.EnvironmentFile, m.policy.MigrationStateRoot)
	if err != nil {
		return nil, err
	}
	if command.TargetComponentRole != domain.ComponentRoleRuntime && componentEnvironmentHasRuntimeEndpoint(env) {
		return nil, fmt.Errorf("component role cannot receive a runtime endpoint")
	}
	profile := command.LifecycleProfile
	noNewPrivileges := profile.NoNewPrivileges
	config := &domain.ContainerConfig{
		Image: command.DesiredImage, Name: command.TargetComponentID, Env: env,
		Labels: componentLifecycleLabels(command), NetworkMode: command.InternalNetwork,
		PortPublishes: append([]domain.ContainerPortPublish(nil), ports...), RestartPolicy: domain.RestartPolicyAlways,
		Cmd:             []string{"serve", "--role", string(command.TargetComponentRole), "--config", "/etc/gordon/role.toml"},
		Aliases:         []string{"gordon-" + string(command.TargetComponentRole)},
		User:            profile.ProcessIdentity.User,
		UsernsMode:      profile.UsernsMode,
		CapDrop:         append([]string(nil), profile.CapDrop...),
		CapAdd:          []string{},
		NoNewPrivileges: &noNewPrivileges,
	}
	if command.TargetComponentRole == domain.ComponentRoleRuntime {
		config.Env = append(config.Env, "GORDON_COMPONENT_ID="+command.TargetComponentID)
		rewritten, socketErr := runtimeComponentSocketMountEnv(config.Env)
		if socketErr != nil {
			return nil, socketErr
		}
		config.Env = rewritten
	}
	mountPlan, err := m.componentMountPlanForCreate(command, ports)
	if err != nil {
		return nil, err
	}
	mountPlan.applyToConfig(config)
	if err := m.prepareComponentMountFilesystem(command); err != nil {
		return nil, err
	}
	if err := m.policy.CheckContainerConfig(command.RuntimeCommandIdentity, "", *config); err != nil {
		return nil, err
	}
	return config, nil
}

// prepareComponentMountFilesystem creates private migration directories required before engine create.
func (m *runtimeComponentLifecycleManager) prepareComponentMountFilesystem(command domain.RuntimeSelfUpdateCommand) error {
	if command.TargetComponentRole != domain.ComponentRoleRuntime && command.TargetComponentRole != domain.ComponentRoleControl {
		return nil
	}
	if strings.TrimSpace(m.policy.MigrationStateRoot) == "" {
		return nil
	}
	root := filepath.Clean(m.policy.MigrationStateRoot)
	if !filepath.IsAbs(root) {
		return fmt.Errorf("migration component root is not configured")
	}
	migrationID := strings.TrimPrefix(command.PolicyDecisionID, "migration:")
	if !domain.ValidComponentMigrationID(migrationID) {
		return fmt.Errorf("invalid migration runtime socket identity")
	}
	if err := prepareMigrationSocketStateDirectory(root, filepath.Join(root, migrationID)); err != nil {
		return err
	}
	if command.TargetComponentRole != domain.ComponentRoleRuntime {
		return nil
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
	if err := m.authorizeRecoveryEdgeAppNetworks(ctx, command); err != nil {
		return err
	}
	containers, err := m.runtime.ListContainers(ctx, true)
	if err != nil {
		return componentLifecycleError("list cutover inventory", err)
	}
	old, target, err := m.cutoverInventory(ctx, command, containers)
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
	completed, finishErr := m.finishInterruptedCutover(ctx, command, target, oldRunning)
	if completed {
		return nil
	}

	restoreErr := m.restoreInterruptedEdgeInventory(ctx, command, target, old, oldRunning)
	retryable := restoreErr == nil
	if recordErr := m.recordCutoverFailure(ctx, command, "cutover_failed", retryable); recordErr != nil {
		return componentLifecycleError("record cutover failure", errors.Join(finishErr, recordErr))
	}
	if !retryable {
		return componentLifecycleError("restore", errors.Join(finishErr, restoreErr))
	}
	if finishErr != nil {
		return componentLifecycleError("finish interrupted cutover", finishErr)
	}
	return componentLifecycleError("recovered", errors.New("cutover rollback completed"))
}

func (m *runtimeComponentLifecycleManager) finishInterruptedCutover(ctx context.Context, command domain.RuntimeSelfUpdateCommand, target *domain.Container, oldRunning bool) (bool, error) {
	if target == nil || oldRunning || !containerPortsMatch(target, command.FinalPortPublishes) || m.healthContainer(ctx, target) != nil {
		return false, nil
	}
	if err := m.connectFinalEdgeAppNetworks(ctx, command, command.EdgeAppNetworks); err != nil {
		return false, err
	}
	if err := m.ensureEdgeAppNetworksAttached(ctx, command, target.Name); err != nil {
		return false, err
	}
	if err := m.recordCutoverSubphase(ctx, command, domain.MigrationCutoverSubphaseBeforeCommit); err != nil {
		return false, err
	}
	if err := m.committer.CommitMigrationCutover(ctx, command); err != nil {
		return false, err
	}
	return true, nil
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

func (m *runtimeComponentLifecycleManager) cutoverInventory(ctx context.Context, command domain.RuntimeSelfUpdateCommand, containers []*domain.Container) (*domain.Container, *domain.Container, error) {
	var old, target *domain.Container
	for _, candidate := range containers {
		if candidate == nil {
			continue
		}
		if candidate.Name == command.OldServingComponentID {
			if candidate.Labels[domain.LabelManaged] != "true" || candidate.Labels[domain.LabelComponent] == "true" {
				return nil, nil, RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "old serving container is not Gordon-managed", CommandID: command.ID, ComponentID: command.OldServingComponentID, Generation: command.Generation}
			}
			old = candidate
		}
		if candidate.Name == command.TargetComponentID {
			inspected, err := m.inspectLifecycleCandidate(ctx, candidate, command)
			if err != nil {
				return nil, nil, err
			}
			target = inspected
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
	old, prepared, err := m.cutoverInventory(ctx, command, containers)
	if err != nil || prepared == nil || !containerPortsMatch(prepared, command.PortPublishes) {
		return errors.New("rollback inventory proof failed")
	}
	if len(command.EdgeAppNetworks) > 0 {
		if err := m.ensureEdgeAppNetworksAttached(ctx, command, prepared.Name); err != nil {
			return errors.New("rollback inventory proof failed")
		}
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
	networks, err := m.listAuthorizedEdgeAppNetworks(ctx, command)
	if err != nil {
		return nil, err
	}
	if len(command.EdgeAppNetworks) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(command.EdgeAppNetworks))
	for _, name := range command.EdgeAppNetworks {
		network := namedNetwork(networks, name)
		if network == nil || !slices.Contains(network.Containers, prepared.Name) {
			return nil, m.deniedAppNetwork(command)
		}
		names = append(names, name)
	}
	return names, nil
}

// authorizeRecoveryEdgeAppNetworks rejects forged or untrusted recovery
// networks before any Stop/Remove/Connect/Commit. Unlike prepared activation,
// recovery may reconnect a missing attachment after this inventory proof.
func (m *runtimeComponentLifecycleManager) authorizeRecoveryEdgeAppNetworks(ctx context.Context, command domain.RuntimeSelfUpdateCommand) error {
	_, err := m.listAuthorizedEdgeAppNetworks(ctx, command)
	return err
}

func (m *runtimeComponentLifecycleManager) listAuthorizedEdgeAppNetworks(ctx context.Context, command domain.RuntimeSelfUpdateCommand) ([]*domain.NetworkInfo, error) {
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
	seen := make(map[string]struct{}, len(command.EdgeAppNetworks))
	for _, name := range command.EdgeAppNetworks {
		if !safeManagedAppNetworkName(name, m.policy.ManagedNetworkPrefix) {
			return nil, m.deniedAppNetwork(command)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, m.deniedAppNetwork(command)
		}
		seen[name] = struct{}{}
		if !validManagedAppNetwork(namedNetwork(networks, name), name, m.policy.ManagedNetworkPrefix) {
			return nil, m.deniedAppNetwork(command)
		}
	}
	return networks, nil
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
	if err := m.ensureEdgeAppNetworksAttached(ctx, command, command.TargetComponentID); err != nil {
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

// ensureEdgeAppNetworksAttached re-authorizes requested networks from current
// managed inventory, then attests attachment. Callers that must join networks
// (final create, prepared restore, interrupted recovery) connect first; this
// proof never issues a redundant Connect when the exact target is already attached.
func (m *runtimeComponentLifecycleManager) ensureEdgeAppNetworksAttached(ctx context.Context, command domain.RuntimeSelfUpdateCommand, containerName string) error {
	networks, err := m.listAuthorizedEdgeAppNetworks(ctx, command)
	if err != nil {
		return err
	}
	if len(command.EdgeAppNetworks) == 0 {
		return nil
	}
	for _, name := range command.EdgeAppNetworks {
		network := namedNetwork(networks, name)
		if network == nil || !slices.Contains(network.Containers, containerName) {
			return errors.New("edge app network attachment proof failed")
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
	restoreErr = errors.Join(restoreErr, m.proveRollbackInventory(ctx, command))
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
	if err := m.runtime.StartContainer(ctx, restored.ID); err != nil {
		return err
	}
	return m.connectFinalEdgeAppNetworks(ctx, command, command.EdgeAppNetworks)
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
		if errors.Is(err, ErrRuntimePolicyDenied) {
			return err
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
	for _, candidate := range containers {
		if candidate != nil && candidate.Name == command.TargetComponentID {
			return m.inspectLifecycleCandidate(ctx, candidate, command)
		}
	}
	return nil, nil
}

// inspectLifecycleCandidate treats list output only as candidate discovery.
// Docker-compatible list responses omit process identity and may expose stale
// mount metadata, so every lifecycle decision is based on a fresh inspect.
func (m *runtimeComponentLifecycleManager) inspectLifecycleCandidate(ctx context.Context, candidate *domain.Container, command domain.RuntimeSelfUpdateCommand) (*domain.Container, error) {
	if candidate == nil || strings.TrimSpace(candidate.ID) == "" {
		return nil, m.deniedLifecycleCandidate(command)
	}
	inspected, err := m.runtime.InspectContainer(ctx, candidate.ID)
	if err != nil {
		return nil, err
	}
	if inspected == nil || inspected.ID != candidate.ID || inspected.Name != command.TargetComponentID || !isManagedLifecycleComponent(inspected, command) {
		return nil, m.deniedLifecycleCandidate(command)
	}
	if err := m.validateExistingLifecycleMounts(inspected, command); err != nil {
		return nil, err
	}
	return inspected, nil
}

func (m *runtimeComponentLifecycleManager) deniedLifecycleCandidate(command domain.RuntimeSelfUpdateCommand) error {
	return RuntimePolicyDeniedError{Reason: RuntimePolicyReasonUnmanagedMutation, Message: "component lifecycle target identity is not Gordon-owned", CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation}
}

func (m *runtimeComponentLifecycleManager) validateExistingLifecycleMounts(container *domain.Container, command domain.RuntimeSelfUpdateCommand) error {
	expectedProfile, ok := domain.FixedRuntimeComponentLifecycleProfile(command.TargetComponentRole)
	if !ok || command.LifecycleProfile.ProcessIdentity != expectedProfile.ProcessIdentity || !validExistingComponentIdentity(container, expectedProfile) {
		return RuntimePolicyDeniedError{
			Reason: RuntimePolicyReasonUnmanagedMutation, Message: "component lifecycle process identity is not allowed",
			CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation,
		}
	}
	var (
		expected map[string]expectedLifecycleMount
		err      error
	)
	if domain.IsRuntimeComponentLifecycleReadAction(command.LifecycleAction) {
		expected, err = m.expectedReadLifecycleMounts(container, command, expectedProfile)
	} else {
		ports := command.PortPublishes
		if containerPortsMatch(container, command.FinalPortPublishes) {
			ports = command.FinalPortPublishes
		}
		plan, planErr := m.componentMountPlanForCreate(command, ports)
		if planErr != nil {
			err = planErr
		} else {
			expected = plan.expectedMounts()
		}
	}
	if err == nil && lifecycleMountsMatch(container.VolumeMounts, expected) {
		return nil
	}
	return RuntimePolicyDeniedError{
		Reason: RuntimePolicyReasonUnsafeHostBindDenied, Message: "component lifecycle mounts are not allowed",
		CommandID: command.ID, ComponentID: command.TargetComponentID, Generation: command.Generation,
	}
}

func (m *runtimeComponentLifecycleManager) expectedReadLifecycleMounts(container *domain.Container, command domain.RuntimeSelfUpdateCommand, profile domain.RuntimeComponentLifecycleProfile) (map[string]expectedLifecycleMount, error) {
	configSource, ok := existingLifecycleMountSource(container.VolumeMounts, "/etc/gordon/role.toml")
	if !ok || approvedComponentConfigFile(command, configSource, m.policy.MigrationStateRoot) != nil {
		return nil, fmt.Errorf("invalid existing component configuration mount")
	}
	profileCommand := command
	profileCommand.LifecycleProfile = profile
	runtimeSocketSource := ""
	if command.TargetComponentRole == domain.ComponentRoleRuntime {
		socketSource, mounted := existingLifecycleMountSource(container.VolumeMounts, "/run/gordon/runtime.sock")
		clean := filepath.Clean(strings.TrimSpace(socketSource))
		if !mounted || !filepath.IsAbs(clean) || clean == string(filepath.Separator) {
			return nil, fmt.Errorf("invalid existing runtime socket mount")
		}
		runtimeSocketSource = clean
	}
	plan, err := m.componentMountPlanForAttestation(profileCommand, configSource, runtimeSocketSource)
	if err != nil {
		return nil, err
	}
	return plan.expectedMounts(), nil
}

func existingLifecycleMountSource(mounts []domain.ContainerVolumeMount, destination string) (string, bool) {
	var source string
	for _, mount := range mounts {
		if mount.Destination != destination {
			continue
		}
		if source != "" || mount.Type != "bind" || strings.TrimSpace(mount.Source) == "" {
			return "", false
		}
		source = mount.Source
	}
	return source, source != ""
}

func lifecycleMountsMatch(actual []domain.ContainerVolumeMount, expected map[string]expectedLifecycleMount) bool {
	if len(actual) != len(expected) {
		return false
	}
	seen := make(map[string]struct{}, len(actual))
	for _, mount := range actual {
		wanted, ok := expected[mount.Destination]
		if !ok || mount.ReadOnly != wanted.readOnly || !slices.Equal(mount.Options, wanted.options) {
			return false
		}
		if _, duplicate := seen[mount.Destination]; duplicate {
			return false
		}
		seen[mount.Destination] = struct{}{}
		if filepath.IsAbs(wanted.source) {
			if mount.Type != "bind" || filepath.Clean(mount.Source) != filepath.Clean(wanted.source) {
				return false
			}
		} else if mount.Type != "volume" || mount.Name != wanted.source {
			return false
		}
	}
	return true
}

func validExistingComponentIdentity(container *domain.Container, profile domain.RuntimeComponentLifecycleProfile) bool {
	return container != nil && container.User == profile.ProcessIdentity.User && container.UsernsMode == profile.UsernsMode &&
		slices.Equal(container.CapDrop, profile.CapDrop) && len(container.CapAdd) == 0 && container.NoNewPrivileges == profile.NoNewPrivileges
}

func validComponentLifecycleTarget(command domain.RuntimeSelfUpdateCommand) bool {
	migrationID := strings.TrimPrefix(command.PolicyDecisionID, "migration:")
	return domain.MatchComponentLifecycleTarget(command.LifecycleAction, command.TargetComponentID, command.TargetComponentRole, migrationID, command.Generation)
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
	actualHash := container.Labels[domain.LabelComponentDesiredStateHash]
	if domain.IsRuntimeComponentLifecycleReadAction(command.LifecycleAction) {
		if actualHash == "" {
			return false
		}
	} else if actualHash != command.DesiredStateHash {
		return false
	}
	return container.Labels[domain.LabelComponentOwner] == "runtime" || container.Labels[domain.LabelComponentOwner] == "migration"
}

const managedControlSecretsPath = "/var/lib/gordon/secrets" // #nosec G101 -- fixed mount destination, not credential material.

func componentGenerationVolumeOptions(command domain.RuntimeSelfUpdateCommand, volumes map[string]string) map[string][]string {
	name := domain.FormatComponentGenerationVolumeName(command.TargetComponentRole, strings.TrimPrefix(command.PolicyDecisionID, "migration:"), command.Generation)
	if volumes["/var/lib/gordon"] != name || len(command.LifecycleProfile.GenerationVolumeOptions) == 0 {
		return nil
	}
	return map[string][]string{"/var/lib/gordon": append([]string(nil), command.LifecycleProfile.GenerationVolumeOptions...)}
}

// componentLifecycleConfigFile permits the dedicated final edge manifest only
// for an activation's validated public bindings. All prepare and rollback paths
// retain edge.toml, preserving the authenticated probe configuration.
func componentLifecycleConfigFile(command domain.RuntimeSelfUpdateCommand, ports []domain.ContainerPortPublish, migrationRoot string) (string, error) {
	path := command.ConfigFile
	if command.TargetComponentRole == domain.ComponentRoleEdge && command.LifecycleAction == domain.RuntimeComponentLifecycleActivate && approvedFinalPortPublishes(ports) && slices.Equal(ports, command.FinalPortPublishes) && filepath.Base(path) == "edge.toml" {
		path = filepath.Join(filepath.Dir(path), "edge-final.toml")
	}
	if err := approvedComponentConfigFile(command, path, migrationRoot); err != nil {
		return "", err
	}
	return path, nil
}

func approvedComponentConfigFile(command domain.RuntimeSelfUpdateCommand, path, migrationRoot string) error {
	clean := filepath.Clean(strings.TrimSpace(path))
	name := string(command.TargetComponentRole) + ".toml"
	if command.TargetComponentRole == domain.ComponentRoleEdge && filepath.Base(clean) == "edge-final.toml" {
		name = "edge-final.toml"
	}
	if !domain.ApprovedGeneratedRolePath(clean, migrationRoot, "config", strings.TrimPrefix(command.PolicyDecisionID, "migration:"), command.Generation, name) {
		return fmt.Errorf("invalid component configuration file")
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("invalid component configuration file")
	}
	return nil
}

const maxComponentLifecycleEnvironmentBytes int64 = 64 << 10

// componentLifecycleEnvironment reads only a runtime-owned generated env file.
// It returns generic errors and never includes values in failures or logs.
func componentLifecycleEnvironment(command domain.RuntimeSelfUpdateCommand, path, migrationRoot string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	clean := filepath.Clean(strings.TrimSpace(path))
	if !domain.ApprovedGeneratedRolePath(clean, migrationRoot, "env", strings.TrimPrefix(command.PolicyDecisionID, "migration:"), command.Generation, string(command.TargetComponentRole)+".env") {
		return nil, fmt.Errorf("invalid component environment file")
	}
	file, err := openPrivateComponentEnvironmentFile(clean, migrationRoot)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxComponentLifecycleEnvironmentBytes+1))
	if err != nil || int64(len(data)) > maxComponentLifecycleEnvironmentBytes {
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

func openPrivateComponentEnvironmentFile(path, migrationRoot string) (*os.File, error) {
	fd, err := openComponentEnvironmentDescriptor(path, migrationRoot)
	if err != nil {
		return nil, fmt.Errorf("invalid component environment file")
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("invalid component environment file")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("invalid component environment file")
	}
	return file, nil
}

func openComponentEnvironmentDescriptor(path, migrationRoot string) (int, error) {
	const fileFlags = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if strings.TrimSpace(migrationRoot) == "" {
		return unix.Open(path, fileFlags, 0)
	}
	root := filepath.Clean(migrationRoot)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return -1, fmt.Errorf("environment path is outside migration root")
	}
	current, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(relative, string(filepath.Separator))
	for _, directory := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(current, directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return -1, openErr
		}
		current = next
	}
	fd, err := unix.Openat(current, parts[len(parts)-1], fileFlags, 0)
	_ = unix.Close(current)
	return fd, err
}

func componentEnvironmentHasRuntimeEndpoint(environment []string) bool {
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && (key == "CONTAINER_HOST" || key == "DOCKER_HOST" || key == "PODMAN_HOST") {
			return true
		}
	}
	return false
}

func runtimeComponentSocketMountEnv(environment []string) ([]string, error) {
	_, rewritten, err := runtimeComponentSocketMount(environment)
	return rewritten, err
}

func runtimeComponentSocketMount(environment []string) (string, []string, error) {
	var source string
	rewritten := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found || (key != "CONTAINER_HOST" && key != "DOCKER_HOST" && key != "PODMAN_HOST") {
			rewritten = append(rewritten, entry)
			continue
		}
		path, err := runtimeSocketSource(value)
		if err != nil {
			return "", nil, err
		}
		if source != "" && source != path {
			return "", nil, fmt.Errorf("conflicting runtime endpoint configuration")
		}
		source = path
	}
	if source == "" {
		return "", nil, fmt.Errorf("runtime endpoint is unavailable")
	}
	rewritten = append(rewritten, "DOCKER_HOST=unix:///run/gordon/runtime.sock")
	return source, rewritten, nil
}

func runtimeSocketSource(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if !strings.HasPrefix(endpoint, "unix://") {
		return "", fmt.Errorf("runtime endpoint must be a local Unix socket")
	}
	path := strings.TrimPrefix(endpoint, "unix://")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("runtime endpoint must be a supported local Unix socket")
	}
	return path, nil
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
