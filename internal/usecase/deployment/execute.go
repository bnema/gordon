package deployment

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// failLogTailLines bounds the log tail attached to service failures.
const failLogTailLines = 50

// failLogTailTimeout bounds the diagnostics read of a failing container.
const failLogTailTimeout = 10 * time.Second

// Deploy executes a preflighted revision: fail-fast across services in
// sorted name order. HTTP services without volumes keep the old container
// serving until the replacement passes readiness, then switch with a
// bounded drain and retire the old container by exact ID. TCP/UDP, mixed,
// and volume-owning services use replacement with interruption. Volumes
// are never deleted: no RemoveVolume call, no volume-deletion flags on
// container removal.
func (s *Service) Deploy(ctx context.Context, input DeployInput) (*DeployResult, error) {
	release, err := s.acquireAppContext(ctx, input.App)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.deployLocked(ctx, input)
}

func (s *Service) deployLocked(ctx context.Context, input DeployInput) (*DeployResult, error) {
	ctx = zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: "Deploy",
		"app":                 input.App,
	})
	log := zerowrap.FromCtx(ctx)

	pinned, op, replayed, err := s.preflightLocked(ctx, input)
	if replayed || err != nil {
		// The key already answered this request: its stored journal is
		// the result and no workload is touched again. A failed preflight
		// returns the journal together with its error.
		return journaledOrNil(input, op, err)
	}
	sort.Slice(pinned, func(i, j int) bool { return pinned[i].name < pinned[j].name })

	active, _, err := s.deps.State.LoadActive(ctx, input.App)
	if err != nil {
		loadErr := fmt.Errorf("deployment: load active: %w", err)
		s.failOperation(ctx, op, loadErr, "error")
		return journaledDeployResult(input, op), loadErr
	}
	rev, err := s.resolveRevision(ctx, input)
	if err != nil {
		s.failOperation(ctx, op, err, "error")
		return journaledDeployResult(input, op), err
	}

	result := &DeployResult{
		Op:       op.Op,
		App:      input.App,
		Revision: rev.Revision,
		Services: map[string]ServiceResult{},
	}
	// Reconcile services the new desired state no longer declares BEFORE
	// publishing anything: a service the operator removed must stop being
	// reachable, and its exact container must be stopped and removed
	// while its data is retained.
	if err := s.reconcileRemovalsForDeploy(ctx, input.App, active, pinned, op, result, log); err != nil {
		return result, err
	}
	for i, p := range pinned {
		if err := s.runServiceStep(ctx, input.App, rev.Revision, i, p, op, active, result, log); err != nil {
			return result, err
		}
	}
	op.Outcome = ComputeOutcome(result.Services)
	op.Warnings = journalWarnings(collectCleanupWarnings(result.Services))
	result.CleanupWarnings = collectCleanupWarnings(result.Services)
	if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
		log.Warn().Err(saveErr).Msg("deployment: failed to record deploy outcome")
	}
	return result, nil
}

// runServiceStep executes one pinned service in the journal: replace,
// publish ACTIVE, then cutover traffic and retire the replaced container.
// The step is recorded as succeeded only after the runtime effect, the
// ACTIVE publication, and the traffic apply all succeeded, so a journaled
// success is never ahead of what is routed.
func (s *Service) runServiceStep(
	ctx context.Context,
	app, revision string,
	index int,
	p pinnedService,
	op *domain.AppOperation,
	active domain.AppActive,
	result *DeployResult,
	log zerowrap.Logger,
) error {
	before := ""
	if eff, ok := active.Services[p.name]; ok {
		before = eff.Container
	}
	svcResult, interrupted := s.deployService(ctx, app, revision, p, op.Op, before, activeStopGrace(active, p.name))
	result.Services[p.name] = svcResult
	if interrupted {
		result.Interrupted = append(result.Interrupted, p.name)
	}
	stepID := "service." + p.name + ".replace"
	step := domain.AppOperationStep{
		ID: stepID, Service: p.name,
		Digest: p.digest, Image: p.runtimeImage,
		Before: before, After: svcResult.After,
	}
	fail := func(err error) error {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		step.Diagnostics = svcResult.Diagnostics
		op.Steps[index+1] = step
		op.Warnings = journalWarnings(collectCleanupWarnings(result.Services))
		op.Outcome = ComputeOutcome(result.Services)
		if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
			log.Warn().Err(saveErr).Msg("deployment: failed to record service failure")
		}
		return err
	}
	if svcResult.Result == "failed" {
		// Fail fast: later services stay unchanged.
		return fail(fmt.Errorf("deployment: service %q failed at %s: %s: %w",
			p.name, stepID, svcResult.Error, domain.ErrAppStateConflict))
	}
	// Publish per-service effective state first: the proxy switches to the
	// new binds at publication. Retire the replaced container only AFTER
	// publication (no outage on crash between the two); a retire failure
	// is a cleanup warning, not an outcome flip.
	if err := s.publishService(ctx, app, revision, p, svcResult, op.Op); err != nil {
		svcResult.Result = "failed"
		svcResult.Error = err.Error()
		result.Services[p.name] = svcResult
		return fail(err)
	}
	if err := s.cutoverService(ctx, app, p, &svcResult, result); err != nil {
		svcResult.Result = "failed"
		svcResult.Error = err.Error()
		result.Services[p.name] = svcResult
		return fail(err)
	}
	result.Services[p.name] = svcResult
	step.State = domain.AppStepSucceeded
	op.Steps[index+1] = step
	if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
		log.Warn().Err(saveErr).Msg("deployment: failed to checkpoint service progress")
	}
	return nil
}

// failOperation records a terminal failure on an already claimed
// journal, so a keyed repeat replays a terminal outcome instead of
// reading a permanently in-flight claim. stepID names the failing phase
// (a service step, a traffic publication, or a bare error). Journal-write
// failures are logged, never returned: the caller already holds a more
// specific error, and the next mutation's recovery pass converges state.
func (s *Service) failOperation(ctx context.Context, op *domain.AppOperation, err error, stepID string) {
	op.Outcome = domain.AppOutcomeFailed
	op.Steps = append(op.Steps, domain.AppOperationStep{
		ID: stepID, State: domain.AppStepFailed, Error: err.Error(),
	})
	if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
		log := zerowrap.FromCtx(ctx)
		log.Warn().Err(saveErr).Str("op", op.Op).Msg("deployment: failed to record operation failure")
	}
}

// failTrafficPublication records that the final graph apply was rejected.
// The workload steps already ran and stay accurate, but the operation is a
// failure: routing did not accept the published state, so a later replay
// of the same key must never read success.
func (s *Service) failTrafficPublication(ctx context.Context, op *domain.AppOperation, err error) {
	s.failOperation(ctx, op, err, "traffic.publish")
}

// journaledOrNil reports a preflight that already answered the request
// key: the stored journal when there is one, the bare error otherwise.
func journaledOrNil(input DeployInput, op *domain.AppOperation, err error) (*DeployResult, error) {
	if op == nil {
		return nil, err
	}
	return journaledDeployResult(input, op), err
}

func journaledDeployResult(input DeployInput, op *domain.AppOperation) *DeployResult {
	return &DeployResult{
		Op:       op.Op,
		App:      input.App,
		Revision: op.InputRevision,
		Outcome:  op.Outcome,
	}
}

// reconcileRemovalsForDeploy journals and applies the removal of services
// the new revision no longer declares. A failure marks the operation
// failed and aborts before any new state is published.
func (s *Service) reconcileRemovalsForDeploy(ctx context.Context, app string, active domain.AppActive, pinned []pinnedService, op *domain.AppOperation, result *DeployResult, log zerowrap.Logger) error {
	removalSteps, removed, warnings, err := s.reconcileRemovedServices(ctx, app, active, pinned)
	op.Warnings = append(op.Warnings, journalWarnings(warnings)...)
	result.CleanupWarnings = append(result.CleanupWarnings, warnings...)
	if err != nil {
		op.Steps = append(op.Steps, removalSteps...)
		op.Outcome = domain.AppOutcomeFailed
		if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
			log.Warn().Err(saveErr).Msg("deployment: failed to record removal failure")
		}
		return err
	}
	if len(removalSteps) == 0 {
		return nil
	}
	op.Steps = append(op.Steps, removalSteps...)
	result.Removed = removed
	if saveErr := s.deps.State.SaveOperation(ctx, *op); saveErr != nil {
		log.Warn().Err(saveErr).Msg("deployment: failed to record service removals")
	}
	return nil
}

// reconcileRemovedServices withdraws and retires every ACTIVE service the
// new revision no longer declares. Traffic is withdrawn first (fail
// closed), then the exact container is inhibited, stopped, and removed
// with its data retained, its backend claims released, and its ACTIVE
// entry deleted. A failure leaves the remaining services untouched and
// stops the deploy before any new state is published.
func (s *Service) reconcileRemovedServices(ctx context.Context, app string, active domain.AppActive, pinned []pinnedService) ([]domain.AppOperationStep, []string, []CleanupWarning, error) {
	desired := make(map[string]struct{}, len(pinned))
	for _, p := range pinned {
		desired[p.name] = struct{}{}
	}
	var names []string
	for name := range active.Services {
		if _, ok := desired[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	steps := make([]domain.AppOperationStep, 0, len(names))
	var cleanupWarnings []CleanupWarning
	for _, name := range names {
		eff := active.Services[name]
		step, warnings, err := s.retireRemovedService(ctx, app, name, eff)
		steps = append(steps, step)
		cleanupWarnings = append(cleanupWarnings, warnings...)
		if err != nil {
			return steps, names, cleanupWarnings, err
		}
		delete(active.Services, name)
	}
	if len(names) == 0 {
		return nil, nil, nil, nil
	}
	if err := s.deps.State.SaveActive(ctx, active); err != nil {
		return steps, names, cleanupWarnings, fmt.Errorf("deployment: persist service removals: %w", err)
	}
	return steps, names, cleanupWarnings, nil
}

// retireRemovedService withdraws one removed service and removes its exact
// container, releasing its claims only after withdrawal succeeded.
func (s *Service) retireRemovedService(ctx context.Context, app, name string, eff domain.AppEffectiveService) (domain.AppOperationStep, []CleanupWarning, error) {
	step := domain.AppOperationStep{
		ID:      "service." + name + ".remove",
		State:   domain.AppStepPending,
		Service: name,
		Before:  eff.Container,
	}
	fail := func(err error) (domain.AppOperationStep, []CleanupWarning, error) {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		return step, nil, err
	}
	if err := s.withdrawForRecovery(ctx, app, name); err != nil {
		return fail(err)
	}
	if eff.Container != "" {
		if err := s.inhibitRecovery(ctx, app, name, eff.Container, "removed", ""); err != nil {
			return fail(err)
		}
		retired := s.retireContainer(ctx, app, retireOptions{
			Service: name, Grace: serviceStopGrace(eff), ClearInhibition: true,
		}, eff.Container)
		if !retired.Gone {
			return fail(fmt.Errorf("deployment: remove %s/%s container %s: %s", app, name, eff.Container, cleanupDetail(retired)))
		}
		step.State = domain.AppStepSucceeded
		step.After = ""
		return step, retired.Warnings, nil
	}
	step.State = domain.AppStepSucceeded
	step.After = ""
	return step, nil, nil
}

// cutoverService applies traffic after publication, then retires the
// replaced container. A traffic failure is returned to the caller, which
// records the failed step: ACTIVE is published but the new service is not
// routable, so the operation must not report success. The old container
// is retained when traffic activation fails.
func (s *Service) cutoverService(ctx context.Context, app string, p pinnedService, svcResult *ServiceResult, result *DeployResult) error {
	if trafficErr := s.refreshTraffic(ctx, app); trafficErr != nil {
		return trafficErr
	}
	if svcResult.Retire != "" {
		drainWithDeadline(ctx, p.spec.StopGrace)
		// The replaced generation keeps its inhibition: only a published
		// replacement may clear it.
		retired := s.retireContainer(ctx, app, retireOptions{
			Service: p.name, Grace: svcResult.RetireGrace,
		}, svcResult.Retire)
		svcResult.CleanupWarnings = append(svcResult.CleanupWarnings, retired.Warnings...)
	}
	return nil
}

// deployService replaces one service. beforeGrace is the effective stop
// grace of the container being replaced. It returns the terminal result
// and whether the service saw interruption.
func (s *Service) deployService(ctx context.Context, app, revision string, p pinnedService, opID, before string, beforeGrace time.Duration) (ServiceResult, bool) {
	if httpEligible(p.spec) {
		return s.deployHTTP(ctx, app, revision, p, opID, before, beforeGrace)
	}
	return s.deployInterrupted(ctx, app, revision, p, opID, before, beforeGrace)
}

// httpEligible reports HTTP-only services without volumes.
func httpEligible(spec domain.AppService) bool {
	if len(spec.HTTP) == 0 || len(spec.TCP) > 0 || len(spec.UDP) > 0 || len(spec.Volumes) > 0 {
		return false
	}
	return true
}

// deployHTTP keeps the old container serving until the replacement passes
// readiness AND the new effective state is published: the proxy switches
// to the new loopback binds at publication, the old container drains,
// and only then is the old container retired by exact ID. Retire never
// precedes publication (no outage window on crash between the two).
func (s *Service) deployHTTP(ctx context.Context, app, revision string, p pinnedService, opID, before string, beforeGrace time.Duration) (ServiceResult, bool) {
	created, binds, udpBinds, err := s.createAndStart(ctx, app, revision, p, opID)
	if err != nil {
		return s.failResult(revision, before, "", err), false
	}
	if err := waitServiceReadyWithDeps(ctx, s.probeDeps(), created.ID, httpReadiness(p.spec), binds); err != nil {
		// Old version keeps serving. Capture redacted diagnostics while
		// the failed replacement still exists, then remove only that
		// candidate.
		tail := s.redactDiagnostics(ctx, app, p, s.logTail(ctx, created.ID))
		cleanup := s.retireCandidate(ctx, app, p.name, created.ID)
		return ServiceResult{
			Result:            "failed",
			EffectiveRevision: revision,
			Before:            before,
			After:             created.ID,
			Error:             err.Error(),
			Diagnostics:       tail,
			CleanupWarnings:   cleanup,
		}, false
	}
	return ServiceResult{
		Result:            "deployed",
		EffectiveRevision: revision,
		Before:            before,
		After:             created.ID,
		BackendBinds:      binds,
		UDPBackendBinds:   udpBinds,
		Retire:            retireContainerID(before, created.ID),
		RetireGrace:       beforeGrace,
	}, false
}

// refreshTraffic rebuilds the proxy host index and applies the full
// HTTP/L4 graph after activation. A failure is fatal to the caller:
// ACTIVE is published but the new service is not routable, so the
// operation must not report success. Any service of this app whose
// fail-closed withdrawal was never applied is withdrawn again first, so
// a later publication never runs on top of unproven forwarding.
func (s *Service) refreshTraffic(ctx context.Context, app string) error {
	if s.deps.Traffic == nil {
		return nil
	}
	for _, service := range s.publication.pending(app) {
		if err := s.withdrawForRecovery(ctx, app, service); err != nil {
			return err
		}
	}
	if err := s.deps.Traffic.RebuildTraffic(ctx); err != nil {
		return fmt.Errorf("deployment: rebuild traffic for %q: %w", app, err)
	}
	return nil
}

// retireContainerID returns the exact container ID to retire after the
func retireContainerID(before, after string) string {
	if before == "" || before == after {
		return ""
	}
	return before
}

// deployInterrupted stops the old instance after preflight, then creates
// the replacement. Volume-owning failures never restart the old image:
// the replacement may already have written data.
func (s *Service) deployInterrupted(ctx context.Context, app, revision string, p pinnedService, opID, before string, beforeGrace time.Duration) (ServiceResult, bool) {
	// A volume-owning replacement may write while the old generation
	// still exists and could be revived by native restart policy.
	// Inhibit that generation durably BEFORE the write can happen, so
	// boot or periodic recovery can never restart the old writer on
	// top of the new one. Cleared once the safe generation is
	// published (publishService) or the operator removes the app.
	inhibited := before != "" && len(p.spec.Volumes) > 0
	if inhibited {
		if err := s.inhibitRecovery(ctx, app, p.name, before, domain.AppInhibitReplacementPending, opID); err != nil {
			return s.failResult(revision, before, "", err), true
		}
	}
	if before != "" {
		// The superseded writer must be confirmed gone before a new
		// writer can touch the same data: a failed retirement aborts the
		// replacement instead of allowing overlapping writers, and the
		// generation keeps its inhibition and its claims.
		retired := s.retireContainer(ctx, app, retireOptions{
			Service: p.name, Grace: beforeGrace,
		}, before)
		if !retired.Gone {
			return s.failResult(revision, before, "", fmt.Errorf("deployment: retire superseded container %s: %s", before, cleanupDetail(retired))), true
		}
	}
	created, binds, udpBinds, err := s.createAndStart(ctx, app, revision, p, opID)
	if err != nil {
		return s.failResult(revision, before, "", err), true
	}
	if err := waitServiceReadyWithDeps(ctx, s.probeDeps(), created.ID, p.spec, binds); err != nil {
		// Capture redacted diagnostics while the candidate still exists,
		// then remove only that candidate.
		tail := s.redactDiagnostics(ctx, app, p, s.logTail(ctx, created.ID))
		cleanup := s.retireCandidate(ctx, app, p.name, created.ID)
		return ServiceResult{
			Result:            "failed",
			EffectiveRevision: revision,
			Before:            before,
			After:             created.ID,
			RestartUnsafe:     len(p.spec.Volumes) > 0,
			Error:             err.Error(),
			Diagnostics:       tail,
			CleanupWarnings:   cleanup,
		}, true
	}
	return ServiceResult{
		Result:            "deployed",
		EffectiveRevision: revision,
		Before:            before,
		After:             created.ID,
		RestartUnsafe:     len(p.spec.Volumes) > 0,
		BackendBinds:      binds,
		UDPBackendBinds:   udpBinds,
	}, true
}

// reserveVolumeOwnership durably records one app-owned volume as
// attached BEFORE the runtime volume is created, so a crash between the
// two leaves a protected record instead of an unowned volume. The
// caller passes the validated incarnation ID from ensureIncarnationID,
// which already assigned it, so no reload is needed for the labels.
func (s *Service) reserveVolumeOwnership(ctx context.Context, app, appID, service, volumeName, runtimeName string) error {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load ownership: %w", err)
	}
	if ownership.App == "" {
		ownership.App = app
	}
	if ownership.ID == "" {
		ownership.ID = appID
	}
	entry := domain.AppOwnedVolume{
		Name:        volumeName,
		Service:     service,
		RuntimeName: runtimeName,
		State:       domain.AppResourceAttached,
	}
	replaced := false
	for i, existing := range ownership.Volumes {
		if existing.RuntimeName == runtimeName {
			ownership.Volumes[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		ownership.Volumes = append(ownership.Volumes, entry)
	}
	if err := s.deps.State.SaveOwnership(ctx, ownership); err != nil {
		return fmt.Errorf("deployment: reserve volume ownership: %w", err)
	}
	return nil
}

// volumeProvenanceLabels is the label set stamped on an app-created
// volume. Ownership is never inferred from the volume name, so the
// labels must carry the full provenance: app, incarnation UUID, and
// service. The managed marker is stamped here as well as merged by the
// adapter, so provenance never depends on adapter behaviour alone.
func volumeProvenanceLabels(app, appID, service, revision string) map[string]string {
	labels := map[string]string{
		domain.LabelManaged:     "true",
		domain.LabelApp:         app,
		domain.LabelAppService:  service,
		domain.LabelAppRevision: revision,
	}
	if appID != "" {
		labels[domain.LabelAppID] = appID
	}
	return labels
}

// createAndStart builds the runtime config and starts the replacement.
// Preflight already pulled and inspected the pinned runtime image.
// Every TCP-capable interface port is published on 127.0.0.1 ephemeral
// plus every UDP interface port on 127.0.0.1/udp ephemeral: readiness
// and the proxy dial these loopback binds rootless-first,
// never container IPs. The runtime keeps native restarts; Gordon
// reconciles intent at boot and in the monitor (accepted decision:
// native runtime restarts plus daemon reconciliation).
func (s *Service) createAndStart(ctx context.Context, app, revision string, p pinnedService, opID string) (*domain.Container, map[int]int, map[int]int, error) {
	env, err := s.serviceEnv(ctx, app, p)
	if err != nil {
		return nil, nil, nil, err
	}
	image := p.runtimeImage
	if image == "" {
		image = p.spec.Image
		if p.digest != "" {
			image = stripImageTag(p.spec.Image) + "@" + p.digest
		}
	}
	// The incarnation network is verified or created before any
	// container exists, so a workload is only ever started on a
	// Gordon-owned network that no other app shares.
	appID, err := s.ensureIncarnationID(ctx, app)
	if err != nil {
		return nil, nil, nil, err
	}
	nets := s.resolveAppNetworks(appID, p.sharedNetworks)
	if err := s.ensureAppNetworks(ctx, app, appID, nets); err != nil {
		return nil, nil, nil, err
	}
	volumes := map[string]string{}
	readOnlyVolumes := map[string]string{}
	for _, vol := range p.spec.Volumes {
		runtimeName := domain.RuntimeVolumeName(app, p.spec.Name, vol.Name)
		// Durable ownership is reserved BEFORE the runtime volume
		// exists: a crash in between leaves a protected record, never
		// an unowned volume that prune could later adopt. The
		// incarnation ID is already validated above, so the labels
		// carry the same ID without another ownership load.
		if err := s.reserveVolumeOwnership(ctx, app, appID, p.spec.Name, vol.Name, runtimeName); err != nil {
			return nil, nil, nil, err
		}
		if err := s.deps.Runtime.CreateVolume(ctx, runtimeName, volumeProvenanceLabels(app, appID, p.spec.Name, revision)); err != nil {
			// CreateVolume is idempotent at the adapter; existence was
			// checked at preflight, so only real backend errors fail here.
			return nil, nil, nil, fmt.Errorf("deployment: create volume %q: %w", vol.Name, err)
		}
		// A declared read-only mount must reach the runtime as read-only:
		// a writable mount would let the service modify protected data.
		if vol.ReadOnly {
			readOnlyVolumes[vol.Path] = runtimeName
			continue
		}
		volumes[vol.Path] = runtimeName
	}
	config := &domain.ContainerConfig{
		Image:           image,
		Name:            domain.LogicalServiceIdentity(app, p.spec.Name) + "--" + shortOp(opID),
		Env:             env,
		Entrypoint:      append([]string(nil), p.spec.Command...),
		Volumes:         volumes,
		ReadOnlyVolumes: readOnlyVolumes,
		Labels:          appLabels(app, p.spec.Name, revision),
		AutoRemove:      false,
		RestartPolicy:   domain.RestartPolicyAlways,
		PortPublishes:   backendPublishes(p.spec),
		NetworkMode:     nets.private,
		Hostname:        p.spec.Name,
		Aliases:         []string{p.spec.Name},
		MemoryLimit:     s.deps.Limits.MemoryBytes,
		NanoCPUs:        s.deps.Limits.NanoCPUs,
		PidsLimit:       s.deps.Limits.PidsLimit,
	}
	created, err := s.deps.Runtime.CreateContainer(ctx, config)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("deployment: create container: %w", err)
	}
	if err := s.connectSharedNetworks(ctx, created.ID, nets); err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return nil, nil, nil, err
	}
	if err := s.deps.Runtime.StartContainer(ctx, created.ID); err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return nil, nil, nil, fmt.Errorf("deployment: start container: %w", err)
	}
	binds, udpBinds, err := s.readBackendBinds(ctx, app, p.spec.Name, created.ID, backendPorts(p.spec))
	if err != nil {
		s.retireCandidate(ctx, app, p.spec.Name, created.ID)
		return nil, nil, nil, err
	}
	return created, binds, udpBinds, nil
}

// backendPublishes collects every interface container port for loopback
// publication: HTTP + TCP interfaces on tcp plus UDP interfaces on udp,
// plus an explicit TCP readiness port. Each publishes on 127.0.0.1
// ephemeral (never public). Deduplicated by (protocol, container port).
func backendPorts(spec domain.AppService) []domain.ContainerBackendPort {
	seen := map[domain.ContainerBackendPort]struct{}{}
	var ports []domain.ContainerBackendPort
	add := func(port int, protocol domain.NetworkProtocol) {
		if port <= 0 {
			return
		}
		key := domain.ContainerBackendPort{ContainerPort: port, Protocol: protocol}
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			ports = append(ports, key)
		}
	}
	for _, h := range spec.HTTP {
		add(h.Port, domain.NetworkProtocolTCP)
	}
	for _, t := range spec.TCP {
		add(t.Port, domain.NetworkProtocolTCP)
	}
	for _, u := range spec.UDP {
		add(u.Port, domain.NetworkProtocolUDP)
	}
	// Readiness probes are TCP-only (validation rejects UDP-only
	// services with tcp/http readiness); the readiness port matches a
	// declared TCP container port.
	add(spec.Readiness.Port, domain.NetworkProtocolTCP)
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Protocol != ports[j].Protocol {
			return ports[i].Protocol < ports[j].Protocol
		}
		return ports[i].ContainerPort < ports[j].ContainerPort
	})
	return ports
}

// backendPublishes maps backend ports to 127.0.0.1 ephemeral publishes.
func backendPublishes(spec domain.AppService) []domain.ContainerPortPublish {
	ports := backendPorts(spec)
	publishes := make([]domain.ContainerPortPublish, 0, len(ports))
	for _, port := range ports {
		publishes = append(publishes, domain.ContainerPortPublish{
			HostIP:        "127.0.0.1",
			HostPort:      0,
			ContainerPort: port.ContainerPort,
			Protocol:      port.Protocol,
		})
	}
	return publishes
}

// readBackendBinds resolves each published container port to its
// 127.0.0.1 host port and registers the Gordon-generated claims
// (owner gordon-backend) in the global checkpoint. Missing binds fail:
// an unbound backend can serve neither readiness nor proxy traffic.
// A registration conflict removes the CANDIDATE container and fails —
// callers must only pass freshly created candidates here, never an
// existing ACTIVE container (see inspectBackendBinds).
func (s *Service) readBackendBinds(ctx context.Context, app, service, containerID string, ports []domain.ContainerBackendPort) (map[int]int, map[int]int, error) {
	if len(ports) == 0 {
		return map[int]int{}, map[int]int{}, nil
	}
	observed, err := s.deps.Runtime.GetContainerBackendBinds(ctx, containerID, ports)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: backend binds: %w", err)
	}
	binds, udpBinds := splitBackendBinds(observed)
	claims := backendClaims(app, service, containerID, observed)
	if len(claims) == 0 {
		return binds, udpBinds, nil
	}
	if err := s.deps.State.RegisterBackendBinds(ctx, claims); err != nil {
		s.retireCandidate(ctx, app, service, containerID)
		return nil, nil, err
	}
	return binds, udpBinds, nil
}

// inspectBackendBinds re-inspects the loopback publishes of an EXISTING
// container and registers the Gordon-generated claims. Unlike
// readBackendBinds it never stops or removes the container: a
// registration conflict fails without touching the ACTIVE workload.
// Missing binds fail: an unbound backend must not stay routable.
func (s *Service) inspectBackendBinds(ctx context.Context, app, service, containerID string, ports []domain.ContainerBackendPort) (map[int]int, map[int]int, error) {
	if len(ports) == 0 {
		return map[int]int{}, map[int]int{}, nil
	}
	observed, err := s.deps.Runtime.GetContainerBackendBinds(ctx, containerID, ports)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: backend binds: %w", err)
	}
	binds, udpBinds := splitBackendBinds(observed)
	claims := backendClaims(app, service, containerID, observed)
	if len(claims) == 0 {
		return binds, udpBinds, nil
	}
	if err := s.deps.State.RegisterBackendBinds(ctx, claims); err != nil {
		return nil, nil, err
	}
	return binds, udpBinds, nil
}

// splitBackendBinds partitions observed binds by protocol. TCP feeds
// readiness and HTTP/TCP proxying; UDP feeds UDP relaying.
func splitBackendBinds(observed []domain.ContainerBackendBind) (map[int]int, map[int]int) {
	binds := map[int]int{}
	udpBinds := map[int]int{}
	for _, bind := range observed {
		if bind.Protocol == domain.NetworkProtocolUDP {
			udpBinds[bind.ContainerPort] = bind.HostPort
		} else {
			binds[bind.ContainerPort] = bind.HostPort
		}
	}
	return binds, udpBinds
}

// backendClaims maps observed binds to Gordon-generated loopback claims
// with the exact protocol (tcp/udp) for the global checkpoint.
func backendClaims(app, service, containerID string, observed []domain.ContainerBackendBind) []domain.AppListenerReservation {
	claims := make([]domain.AppListenerReservation, 0, len(observed))
	for _, bind := range observed {
		claims = append(claims, domain.AppListenerReservation{
			Proto:       string(bind.Protocol),
			IP:          "127.0.0.1",
			Port:        bind.HostPort,
			Service:     service,
			App:         app,
			Owner:       domain.OwnerGordonBackend,
			ContainerID: containerID,
		})
	}
	return claims
}

// stripImageTag removes any tag or digest suffix from an image reference.
// colon is a registry port (localhost:15500/e2e/web:v1 strips to
// localhost:15500/e2e/web).
func stripImageTag(ref string) string {
	if base, _, ok := strings.Cut(ref, "@"); ok {
		return base
	}
	lastSlash := strings.LastIndex(ref, "/")
	if idx := strings.LastIndex(ref, ":"); idx > lastSlash {
		return ref[:idx]
	}
	return ref
}

// pullImage fetches the pinned image from the installation registry.
// External refs pull anonymously; installation-registry refs use the
// internal credentials. A missing registry config disables pulling
// (tests and pre-pulled environments).
func (s *Service) pullImage(ctx context.Context, image string) (string, error) {
	if err := s.validateImageSource(image, ""); err != nil {
		return "", err
	}
	if s.deps.Registry.Domain == "" {
		return image, nil
	}
	host, _, _ := strings.Cut(image, "/")
	if host != s.deps.Registry.Domain {
		if err := s.deps.Runtime.PullImage(ctx, image); err != nil {
			return "", fmt.Errorf("deployment: pull image %q: %w", image, err)
		}
		return image, nil
	}
	if s.deps.Registry.Username == "" {
		if err := s.deps.Runtime.PullImage(ctx, image); err != nil {
			return "", fmt.Errorf("deployment: pull image %q: %w", image, err)
		}
		return image, nil
	}
	pullImage := image
	if s.deps.Registry.PullAddress != "" {
		pullImage = s.deps.Registry.PullAddress + strings.TrimPrefix(image, s.deps.Registry.Domain)
	}
	if err := s.deps.Runtime.PullImageWithAuth(ctx, pullImage, s.deps.Registry.Username, s.deps.Registry.Password); err != nil {
		return "", fmt.Errorf("deployment: pull image %q: %w", image, err)
	}
	return pullImage, nil
}

// serviceEnv resolves the service environment: the captured revision's
// app-wide public env on every service, plus that service's own secret
// values (memory only, UUID-keyed paths). Keys are emitted sorted and
// deterministic. Overlapping public/secret keys are invalid with no
// precedence; preflight rejects them before any mutation.
func (s *Service) serviceEnv(ctx context.Context, app string, p pinnedService) ([]string, error) {
	spec := p.spec
	envMap := make(map[string]string, len(p.appEnv)+len(spec.Secrets))
	for key, value := range p.appEnv {
		envMap[key] = value
	}
	keys := make([]string, 0, len(spec.Secrets))
	for envKey := range spec.Secrets {
		keys = append(keys, envKey)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return sortedEnv(envMap), nil
	}
	id, err := s.appSecretID(ctx, app)
	if err != nil {
		return nil, err
	}
	for _, envKey := range keys {
		if _, overlap := envMap[envKey]; overlap {
			return nil, fmt.Errorf("deployment: env key %q collides with a secret key in service %q: %w", envKey, spec.Name, domain.ErrInvalidAppSpec)
		}
		path := domain.AppSecretPathForID(id, app, spec.Name, spec.Secrets[envKey])
		value, err := s.deps.Secrets.GetSecret(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("deployment: secret %s (env %s) missing: %w", path, envKey, domain.ErrAppSecretMissing)
		}
		envMap[envKey] = value
	}
	return sortedEnv(envMap), nil
}

// sortedEnv renders a KEY=value list in sorted key order. Values stay in
// memory for container creation only; nothing is persisted or logged.
func sortedEnv(envMap map[string]string) []string {
	if len(envMap) == 0 {
		return nil
	}
	keys := make([]string, 0, len(envMap))
	for key := range envMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+envMap[key])
	}
	return env
}

// publishService writes the per-service effective record after cutover.
func (s *Service) publishService(ctx context.Context, app, revision string, p pinnedService, result ServiceResult, opID string) error {
	if app == "" {
		return fmt.Errorf("deployment: publish active: empty app: %w", domain.ErrAppStateConflict)
	}
	active, ok, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load active: %w", err)
	}
	if !ok {
		active = domain.AppActive{App: app, Services: map[string]domain.AppEffectiveService{}}
	}
	if active.Services == nil {
		active.Services = map[string]domain.AppEffectiveService{}
	}
	active.Services[p.name] = domain.AppEffectiveService{
		EffectiveRevision: revision,
		ActivatedBy:       opID,
		ActivatedAt:       time.Now().UTC(),
		Image:             p.spec.Image,
		Digest:            p.digest,
		Container:         result.After,
		Spec:              p.spec,
		BackendBinds:      result.BackendBinds,
		UDPBackendBinds:   result.UDPBackendBinds,
	}
	active.ConvergedRevision = revision
	converged := true
	for _, svc := range active.Services {
		if svc.EffectiveRevision != revision {
			converged = false
			break
		}
	}
	active.Converged = converged
	if err := s.deps.State.SaveActive(ctx, active); err != nil {
		return fmt.Errorf("deployment: publish active: %w", err)
	}
	if err := s.recordOwnership(ctx, app, p); err != nil {
		return err
	}
	// The superseded generation is now safely replaced: clearing its
	// inhibition is what makes this generation authoritative. A failed
	// publication leaves the marker in place.
	if result.Before != "" && result.Before != result.After {
		if err := s.clearRecoveryInhibition(ctx, app, p.name, result.Before); err != nil {
			return err
		}
	}
	return nil
}

// recordOwnership stamps volume/secret/network ownership after cutover.
func (s *Service) recordOwnership(ctx context.Context, app string, p pinnedService) error {
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		return err
	}
	if ownership.App == "" {
		ownership.App = app
	}
	// ownership.ID arrives from LoadOwnership (store-assigned UUID);
	// empty only for legacy records predating UUIDs.
	volumes := append([]domain.AppOwnedVolume(nil), ownership.Volumes...)
	seen := map[string]int{}
	for i, vol := range volumes {
		seen[vol.Name] = i
	}
	for _, vol := range p.spec.Volumes {
		entry := domain.AppOwnedVolume{
			Name:        vol.Name,
			Service:     p.spec.Name,
			RuntimeName: domain.RuntimeVolumeName(app, p.spec.Name, vol.Name),
			State:       domain.AppResourceAttached,
		}
		if idx, ok := seen[vol.Name]; ok {
			volumes[idx] = entry
		} else {
			seen[vol.Name] = len(volumes)
			volumes = append(volumes, entry)
		}
	}
	ownership.Volumes = volumes
	secrets := append([]domain.AppOwnedSecret(nil), ownership.Secrets...)
	secretSeen := map[string]int{}
	for i, secret := range secrets {
		secretSeen[secret.Service+"\x00"+secret.Env] = i
	}
	for envKey, name := range p.spec.Secrets {
		entry := domain.AppOwnedSecret{
			Service: p.spec.Name,
			Env:     envKey,
			Name:    name,
			Path:    domain.AppSecretPathForID(ownership.ID, app, p.spec.Name, name),
			State:   domain.AppResourceAttached,
		}
		key := p.spec.Name + "\x00" + envKey
		if idx, ok := secretSeen[key]; ok {
			secrets[idx] = entry
		} else {
			secretSeen[key] = len(secrets)
			secrets = append(secrets, entry)
		}
	}
	ownership.Secrets = secrets
	ownership.Images = recordImageOwnership(ownership.Images, p)
	recordNetworkOwnership(&ownership, s.deps.Networks.Prefix, p.sharedNetworks)
	if ownership.Services == nil {
		ownership.Services = map[string]domain.AppServiceRecovery{}
	}
	ownership.Services[p.spec.Name] = domain.AppServiceRecovery{RestartUnsafe: len(p.spec.Volumes) > 0}
	if err := s.deps.State.SaveOwnership(ctx, ownership); err != nil {
		return fmt.Errorf("deployment: record ownership: %w", err)
	}
	return nil
}

// recordImageOwnership stamps one service's pinned image into the app's
// owned-image list. Positive durable ownership is what later authorizes
// runtime image prune; image labels alone never do.
func recordImageOwnership(existing []domain.AppOwnedImage, p pinnedService) []domain.AppOwnedImage {
	entry := domain.AppOwnedImage{
		Service:   p.spec.Name,
		Reference: p.spec.Image,
		Digest:    p.digest,
		State:     domain.AppResourceAttached,
	}
	out := append([]domain.AppOwnedImage(nil), existing...)
	for i, image := range out {
		if image.Service == entry.Service {
			out[i] = entry
			return out
		}
	}
	return append(out, entry)
}

// failResult builds a preflight/create failure before any container exists.
func (s *Service) failResult(revision, before, after string, err error) ServiceResult {
	return ServiceResult{
		Result:            "failed",
		EffectiveRevision: revision,
		Before:            before,
		After:             after,
		Error:             err.Error(),
	}
}

// logTail returns the bounded recent log tail of the failing container.
// The read is bounded by the caller's deadline and by an internal cap so
// a slow or hung log stream cannot stall the operation.
func (s *Service) logTail(ctx context.Context, containerID string) []string {
	if containerID == "" || ctx.Err() != nil {
		return nil
	}
	tailCtx, cancel := context.WithTimeout(ctx, failLogTailTimeout)
	defer cancel()
	stream, err := s.deps.Runtime.GetContainerLogs(tailCtx, containerID, false)
	if err != nil {
		return nil
	}
	defer func() { _ = stream.Close() }()
	lines, err := tailLines(stream, failLogTailLines)
	if err != nil {
		return nil
	}
	return lines
}

// redactDiagnostics replaces every value of this service's secrets in the
// given log lines. Secret values are read into memory only. When a secret
// cannot be read, diagnostics are dropped entirely: unredacted application
// output is never persisted.
func (s *Service) redactDiagnostics(ctx context.Context, app string, p pinnedService, lines []string) []string {
	if len(lines) == 0 {
		return nil
	}
	if len(p.spec.Secrets) == 0 || s.deps.Secrets == nil {
		return lines
	}
	id, err := s.appSecretID(ctx, app)
	if err != nil {
		return nil
	}
	redacted := append([]string(nil), lines...)
	for _, name := range p.spec.Secrets {
		path := domain.AppSecretPathForID(id, app, p.spec.Name, name)
		value, err := s.deps.Secrets.GetSecret(ctx, path)
		if err != nil || value == "" {
			return nil
		}
		for i, line := range redacted {
			redacted[i] = strings.ReplaceAll(line, value, "[redacted]")
		}
	}
	return redacted
}

// tailLines keeps the last N lines; logs are untrusted app output.
func tailLines(r io.Reader, n int) ([]string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[len(lines)-n:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// httpReadiness forces the HTTP path for eligible cutover checks.
func httpReadiness(spec domain.AppService) domain.AppService {
	if spec.Readiness.Type == "" || spec.Readiness.Type == domain.AppReadinessNone {
		spec.Readiness.Type = domain.AppReadinessHTTP
	}
	return spec
}

// drainWithDeadline pauses briefly so in-flight HTTP requests finish.
func drainWithDeadline(ctx context.Context, grace time.Duration) {
	if grace <= 0 {
		grace = domain.AppDefaultStopGrace
	}
	if grace > time.Second {
		grace = time.Second
	}
	select {
	case <-time.After(grace):
	case <-ctx.Done():
	}
}

// appLabels stamps engine ownership on every created container.
func appLabels(app, service, revision string) map[string]string {
	return map[string]string{
		domain.LabelManaged:     "true",
		domain.LabelApp:         app,
		domain.LabelAppService:  service,
		domain.LabelAppRevision: revision,
	}
}

// shortOp keeps container names short but unique per op.
func shortOp(opID string) string {
	trimmed := strings.TrimPrefix(opID, "op-")
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	if trimmed == "" {
		return "op"
	}
	return trimmed
}
