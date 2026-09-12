package deployment

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/bnema/zerowrap"

	"github.com/bnema/gordon/internal/domain"
)

// LifecycleResult carries the terminal outcome of a lifecycle verb.
type LifecycleResult struct {
	Op       string
	App      string
	Verb     string
	Outcome  string
	Services map[string]ServiceResult
}

// Stop persists the durable stopped intent first, then stops and removes
// exact active containers by ID. Volumes and secrets are retained.
func (s *Service) Stop(ctx context.Context, app, opID string) (*LifecycleResult, error) {
	release, err := s.acquireAppContext(ctx, app)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.stopLocked(ctx, app, opID)
}

func (s *Service) stopLocked(ctx context.Context, app, opID string) (*LifecycleResult, error) {
	ctx = lifecycleCtx(ctx, "Stop", app)
	log := zerowrap.FromCtx(ctx)
	if err := s.deps.State.Recover(ctx); err != nil {
		return nil, fmt.Errorf("deployment: recover before stop: %w", err)
	}
	op := domain.AppOperation{
		Kind: "stop", App: app,
		StartedAt: time.Now().UTC(),
		Request:   domain.AppOperationRequestFor("stop", app, "", ""),
	}
	op, owned, err := s.claimOperation(ctx, opID, op, []domain.AppOperationStep{{ID: "intent.stopped", State: domain.AppStepPending}})
	if err != nil {
		return nil, err
	}
	if !owned {
		return replayedLifecycleResult(op), replayError(op)
	}
	if err := s.deps.State.SaveIntent(ctx, domain.AppStopIntent{
		App: app, Stopped: true, UpdatedBy: op.Op, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		intentErr := fmt.Errorf("deployment: persist stopped intent: %w", err)
		s.failOperation(ctx, &op, intentErr, "error")
		return nil, intentErr
	}
	op.Steps[0].State = domain.AppStepSucceeded
	active, _, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		activeErr := fmt.Errorf("deployment: load active: %w", err)
		s.failOperation(ctx, &op, activeErr, "error")
		return nil, activeErr
	}
	result := &LifecycleResult{Op: op.Op, App: app, Verb: "stop", Services: map[string]ServiceResult{}}
	var failures []string
	for _, name := range sortedServiceNames(active) {
		container := active.Services[name].Container
		step, err := s.stopService(ctx, app, name, active.Services[name])
		op.Steps = append(op.Steps, step)
		if err != nil {
			result.Services[name] = ServiceResult{Result: "failed", Before: container, Error: err.Error()}
			failures = append(failures, name)
			continue
		}
		result.Services[name] = ServiceResult{Result: "deployed", Before: container, After: ""}
	}
	op.Outcome = ComputeOutcome(result.Services)
	op.Warnings = journalWarnings(result.Services)
	if err := s.deps.State.SaveOperation(ctx, op); err != nil {
		log.Warn().Err(err).Msg("deployment: failed to record stop outcome")
	}
	if err := s.refreshTraffic(ctx, app); err != nil {
		s.failTrafficPublication(ctx, &op, err)
		return result, err
	}
	if len(failures) > 0 {
		return result, fmt.Errorf("deployment: stop failed for %s: %w", strings.Join(failures, ", "), domain.ErrAppStateConflict)
	}
	return result, nil
}

// stopService withdraws one service, then retires its exact container
// with the effective grace. A withdrawal or runtime failure is reported,
// never treated as a successful stop. The confirmed disappearance
// releases the container's backend claims and its recovery inhibition:
// a stopped app is never revived by recovery.
func (s *Service) stopService(ctx context.Context, app, name string, eff domain.AppEffectiveService) (domain.AppOperationStep, error) {
	container := eff.Container
	step := domain.AppOperationStep{ID: "service." + name + ".stop", State: domain.AppStepPending, Before: container}
	fail := func(err error) (domain.AppOperationStep, error) {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		return step, err
	}
	if err := s.withdrawForRecovery(ctx, app, name); err != nil {
		return fail(err)
	}
	if container != "" {
		retired := s.retireContainer(ctx, app, retireOptions{
			Service: name, Grace: serviceStopGrace(eff), ClearInhibition: true,
		}, container)
		if !retired.Gone {
			return fail(fmt.Errorf("deployment: stop %s/%s container %s: %s", app, name, container, cleanupDetail(retired)))
		}
		step.After = container
	}
	step.State = domain.AppStepSucceeded
	return step, nil
}

// Start clears the stopped intent and ensures running from active records.
// It starts missing/stopped instances without duplicating running ones
// and never activates pending desired revisions.
func (s *Service) Start(ctx context.Context, app, opID string) (*LifecycleResult, error) {
	release, err := s.acquireAppContext(ctx, app)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.startLocked(ctx, app, opID)
}

func (s *Service) startLocked(ctx context.Context, app, opID string) (*LifecycleResult, error) {
	ctx = lifecycleCtx(ctx, "Start", app)
	log := zerowrap.FromCtx(ctx)
	if err := s.deps.State.Recover(ctx); err != nil {
		return nil, fmt.Errorf("deployment: recover before start: %w", err)
	}
	op := domain.AppOperation{
		Kind: "start", App: app,
		StartedAt: time.Now().UTC(),
		Request:   domain.AppOperationRequestFor("start", app, "", ""),
	}
	op, owned, err := s.claimOperation(ctx, opID, op, []domain.AppOperationStep{{ID: "intent.running", State: domain.AppStepPending}})
	if err != nil {
		return nil, err
	}
	if !owned {
		return replayedLifecycleResult(op), replayError(op)
	}
	if err := s.deps.State.SaveIntent(ctx, domain.AppStopIntent{
		App: app, Stopped: false, UpdatedBy: op.Op, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		intentErr := fmt.Errorf("deployment: clear stopped intent: %w", err)
		s.failOperation(ctx, &op, intentErr, "error")
		return nil, intentErr
	}
	op.Steps[0].State = domain.AppStepSucceeded
	active, _, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		activeErr := fmt.Errorf("deployment: load active: %w", err)
		s.failOperation(ctx, &op, activeErr, "error")
		return nil, activeErr
	}
	result := &LifecycleResult{Op: op.Op, App: app, Verb: "start", Services: map[string]ServiceResult{}}
	for _, name := range sortedServiceNames(active) {
		eff := active.Services[name]
		step := domain.AppOperationStep{ID: "service." + name + ".start", State: domain.AppStepPending, Before: eff.Container}
		if s.ensureServiceRunning(ctx, app, opID, name, eff, &step, result) {
			op.Steps = append(op.Steps, step)
			continue
		}
		op.Steps = append(op.Steps, step)
	}
	op.Outcome = ComputeOutcome(result.Services)
	op.Warnings = journalWarnings(result.Services)
	if err := s.deps.State.SaveOperation(ctx, op); err != nil {
		log.Warn().Err(err).Msg("deployment: failed to record start outcome")
	}
	if err := s.refreshTraffic(ctx, app); err != nil {
		s.failTrafficPublication(ctx, &op, err)
		return result, err
	}
	return result, nil
}

// Restart restarts from pinned digests without re-resolution. Empty
// service means all services in sorted order.
func (s *Service) Restart(ctx context.Context, app, service, opID string) (*LifecycleResult, error) {
	release, err := s.acquireAppContext(ctx, app)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.restartLocked(ctx, app, service, opID)
}

func (s *Service) restartLocked(ctx context.Context, app, service, opID string) (*LifecycleResult, error) {
	ctx = lifecycleCtx(ctx, "Restart", app)
	if err := s.deps.State.Recover(ctx); err != nil {
		return nil, fmt.Errorf("deployment: recover before restart: %w", err)
	}
	active, ok, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		return nil, fmt.Errorf("deployment: load active: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("deployment: app %q was never deployed: %w", app, domain.ErrAppStateConflict)
	}
	names := sortedServiceNames(active)
	if service != "" {
		if _, ok := active.Services[service]; !ok {
			return nil, fmt.Errorf("deployment: service %q not active: %w", service, domain.ErrAppStateConflict)
		}
		names = []string{service}
	}
	op := domain.AppOperation{
		Kind: "restart", App: app,
		StartedAt: time.Now().UTC(),
		Request:   domain.AppOperationRequestFor("restart", app, "", service),
	}
	op, owned, err := s.claimOperation(ctx, opID, op, nil)
	if err != nil {
		return nil, err
	}
	if !owned {
		return replayedLifecycleResult(op), replayError(op)
	}
	result := &LifecycleResult{Op: op.Op, App: app, Verb: "restart", Services: map[string]ServiceResult{}}
	var failures []string
	for _, name := range names {
		eff := active.Services[name]
		step, svcResult := s.restartOneService(ctx, app, name, eff)
		op.Steps = append(op.Steps, step)
		result.Services[name] = svcResult
		if svcResult.Result != "deployed" {
			failures = append(failures, name)
		}
	}
	op.Outcome = ComputeOutcome(result.Services)
	op.Warnings = journalWarnings(result.Services)
	if err := s.deps.State.SaveOperation(ctx, op); err != nil {
		return result, fmt.Errorf("deployment: persist restart journal: %w", err)
	}
	if err := s.refreshTraffic(ctx, app); err != nil {
		s.failTrafficPublication(ctx, &op, err)
		return result, err
	}
	if len(failures) > 0 {
		return result, fmt.Errorf("deployment: restart failed for %s: %w", strings.Join(failures, ", "), domain.ErrAppStateConflict)
	}
	return result, nil
}

// restartOneService withdraws one service, restarts its exact container,
// re-inspects its binds, and verifies readiness before it may be published
// again. Every failure leaves the service withdrawn with its recorded binds
// cleared, so a failed restart is never served.
func (s *Service) restartOneService(ctx context.Context, app, name string, eff domain.AppEffectiveService) (domain.AppOperationStep, ServiceResult) {
	step := domain.AppOperationStep{ID: "service." + name + ".restart", State: domain.AppStepPending, Before: eff.Container}
	result := ServiceResult{Result: "failed", Before: eff.Container, After: eff.Container}
	fail := func(msg string) (domain.AppOperationStep, ServiceResult) {
		step.State = domain.AppStepFailed
		step.Error = msg
		result.Error = msg
		return step, result
	}
	if eff.Container == "" {
		result.After = ""
		return fail("no active container")
	}
	if err := s.refuseInhibitedRestart(ctx, app, name, eff.Container); err != nil {
		_ = s.withdrawForRecovery(ctx, app, name)
		return fail(err.Error())
	}
	// Withdraw before restarting: a restarting or unverified generation
	// must not keep receiving traffic.
	if err := s.withdrawForRecovery(ctx, app, name); err != nil {
		return fail(err.Error())
	}
	if err := s.deps.Runtime.RestartContainer(ctx, eff.Container, serviceStopGrace(eff)); err != nil {
		s.clearServiceBinds(ctx, app, name)
		return fail(err.Error())
	}
	// Ephemeral loopback binds are not guaranteed stable across a runtime
	// restart: re-inspect before probing so the proxy never dials a stale
	// bind.
	binds, udpBinds, err := s.refreshBackendBinds(ctx, app, name, eff)
	if err != nil {
		s.clearServiceBinds(ctx, app, name)
		return fail(err.Error())
	}
	if err := waitServiceReadyWithDeps(ctx, s.probeDeps(), eff.Container, eff.Spec, binds); err != nil {
		s.clearServiceBinds(ctx, app, name)
		return fail(err.Error())
	}
	step.State = domain.AppStepSucceeded
	step.After = eff.Container
	return step, ServiceResult{Result: "deployed", Before: eff.Container, After: eff.Container, BackendBinds: binds, UDPBackendBinds: udpBinds}
}

// Remove withdraws workloads by exact container ID; volumes, secrets, and
// ownership records are retained under the old UUID. The public name is
// freed for reuse; a new app never implicitly adopts retained resources.
// Stopped intent and a durable recovery inhibition per removed container
// are persisted BEFORE any runtime effect, so native restart policy can
// never revive a removed generation into service.
func (s *Service) Remove(ctx context.Context, app, opID string) (*LifecycleResult, error) {
	release, err := s.acquireAppContext(ctx, app)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.removeLocked(ctx, app, opID)
}

func (s *Service) removeLocked(ctx context.Context, app, opID string) (*LifecycleResult, error) {
	ctx = lifecycleCtx(ctx, "Remove", app)
	log := zerowrap.FromCtx(ctx)
	if err := s.deps.State.Recover(ctx); err != nil {
		return nil, fmt.Errorf("deployment: recover before remove: %w", err)
	}
	op := domain.AppOperation{
		Kind: "remove", App: app,
		StartedAt: time.Now().UTC(),
		Request:   domain.AppOperationRequestFor("remove", app, "", ""),
	}
	op, owned, err := s.claimOperation(ctx, opID, op, nil)
	if err != nil {
		return nil, err
	}
	if !owned {
		return replayedLifecycleResult(op), replayError(op)
	}
	active, _, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		activeErr := fmt.Errorf("deployment: load active: %w", err)
		s.failOperation(ctx, &op, activeErr, "error")
		return nil, activeErr
	}
	result := &LifecycleResult{Op: op.Op, App: app, Verb: "remove", Services: map[string]ServiceResult{}}
	// Durable stopped intent and per-container inhibition precede every
	// runtime effect: a crash between here and the container stop must
	// never leave a generation that recovery could restart.
	if err := s.deps.State.SaveIntent(ctx, domain.AppStopIntent{
		App: app, Stopped: true, UpdatedBy: op.Op, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		intentErr := fmt.Errorf("deployment: persist stopped intent before remove: %w", err)
		s.failOperation(ctx, &op, intentErr, "error")
		return nil, intentErr
	}
	steps, err := s.removeServiceContainers(ctx, app, op.Op, active, result)
	if err != nil {
		op.Steps = steps
		s.failOperation(ctx, &op, err, "error")
		return nil, err
	}
	op.Steps = steps
	// End the incarnation atomically: the ownership record is archived
	// with its resources retained under the old UUID, the app UUID is
	// reset so a name reuse allocates a new incarnation, and desired,
	// active, intent, and staged state are cleared. Without this a
	// reapply would reuse the UUID and inherit the old secrets and
	// volumes.
	if err := s.deps.State.RetireApp(ctx, app); err != nil {
		retireErr := fmt.Errorf("deployment: retire incarnation: %w", err)
		s.failOperation(ctx, &op, retireErr, "error")
		return nil, retireErr
	}
	op.Outcome = domain.AppOutcomeSuccess
	op.Warnings = journalWarnings(result.Services)
	if err := s.deps.State.SaveOperation(ctx, op); err != nil {
		log.Warn().Err(err).Msg("deployment: failed to record remove outcome")
	}
	if err := s.refreshTraffic(ctx, app); err != nil {
		s.failTrafficPublication(ctx, &op, err)
		return result, err
	}
	return result, nil
}

// replayedLifecycleResult projects a replayed journal into a lifecycle
// result without touching any workload.
func replayedLifecycleResult(op domain.AppOperation) *LifecycleResult {
	return &LifecycleResult{Op: op.Op, App: op.App, Verb: op.Kind, Outcome: op.Outcome}
}

// refuseInhibitedRestart blocks a restart of a generation whose recovery
// is durably inhibited: a replacement may already have written to the
// volume this generation still owns, so reviving it could corrupt newer
// data. Deploying a new revision supersedes the marker safely.
func (s *Service) refuseInhibitedRestart(ctx context.Context, app, name, containerID string) error {
	inhibited, err := s.recoveryInhibited(ctx, app, name, containerID)
	if err != nil {
		return err
	}
	if inhibited {
		return fmt.Errorf("deployment: restart %q/%q refused: recovery inhibited for container %s", app, name, containerID)
	}
	return nil
}

// removeServiceContainers persists one inhibition per removed container
// before stopping and removing it by exact ID. Volumes are never deleted.
// A container that could not be confirmed gone fails the remove: ACTIVE,
// stopped intent, and the inhibition stay in place so the periodic pass
// keeps converging the survivor instead of losing track of it.
func (s *Service) removeServiceContainers(ctx context.Context, app, opID string, active domain.AppActive, result *LifecycleResult) ([]domain.AppOperationStep, error) {
	var steps []domain.AppOperationStep
	for _, name := range sortedServiceNames(active) {
		container := active.Services[name].Container
		step := domain.AppOperationStep{ID: "service." + name + ".remove", State: domain.AppStepPending, Before: container}
		if container != "" {
			if err := s.inhibitRecovery(ctx, app, name, container, "removed", opID); err != nil {
				return nil, err
			}
			retired := s.retireContainer(ctx, app, retireOptions{
				Service: name, Grace: serviceStopGrace(active.Services[name]), ClearInhibition: true,
			}, container)
			if !retired.Gone {
				return nil, fmt.Errorf("deployment: remove %q/%q: container %s not confirmed gone: %s", app, name, container, cleanupDetail(retired))
			}
		}
		step.State = domain.AppStepSucceeded
		steps = append(steps, step)
		result.Services[name] = ServiceResult{Result: "deployed", Before: container, After: ""}
	}
	return steps, nil
}

// ReconcileBoot reconciles apps intended to run after a daemon boot:
// every non-stopped app with ACTIVE services is verified through Start,
// including all-running apps: ephemeral loopback binds may shift across
// any runtime restart while the daemon is away, so recorded binds are
// re-inspected (never trusted) before the proxy can dial them.
// Recovery first; no duplication of running instances; no activation of
// pending desired revisions. Explicitly stopped apps stay stopped.
// A failing app never blocks the verification of the following apps;
// failures are aggregated and every unverified bind is withdrawn from
// ACTIVE (see failServiceStep), so the post-boot host index rebuild
// projects fail-closed for all of them.
func (s *Service) ReconcileBoot(ctx context.Context) error {
	ctx = lifecycleCtx(ctx, "ReconcileBoot", "boot")
	if err := s.deps.State.Recover(ctx); err != nil {
		return fmt.Errorf("deployment: recover before boot reconcile: %w", err)
	}
	apps, err := s.deps.State.ListApps(ctx)
	if err != nil {
		return fmt.Errorf("deployment: list apps: %w", err)
	}
	var failures []error
	for _, app := range apps {
		if err := s.reconcileBootApp(ctx, app); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// reconcileBootApp verifies one app at boot under the app lock. Explicitly
// stopped apps and apps without ACTIVE services are skipped without error.
// Stopped intent is converged: a container revived by native restart
// policy while the daemon was away is stopped again, never restarted.
func (s *Service) reconcileBootApp(ctx context.Context, app string) error {
	release, err := s.acquireAppContext(ctx, app)
	if err != nil {
		return err
	}
	defer release()
	intent, err := s.deps.State.LoadIntent(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load intent: %w", err)
	}
	active, ok, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		return fmt.Errorf("deployment: load active: %w", err)
	}
	if !ok || len(active.Services) == 0 {
		return nil
	}
	if intent.Stopped {
		for _, name := range sortedServiceNames(active) {
			if err := s.convergeStoppedService(ctx, app, name, active.Services[name]); err != nil {
				return fmt.Errorf("deployment: boot stopped convergence %q/%q: %w", app, name, err)
			}
		}
		return nil
	}
	result, err := s.startLocked(ctx, app, "")
	if err != nil {
		return fmt.Errorf("deployment: boot start %q: %w", app, err)
	}
	for name, svc := range result.Services {
		if svc.Result == "failed" {
			return fmt.Errorf("deployment: boot verify %q/%q: %s", app, name, svc.Error)
		}
	}
	return nil
}

// ensureServiceRunning starts one service when needed. Running
// containers are bind-verified before success: ephemeral loopback binds
// may shift across any runtime restart (stop/start, daemon absence,
// host reboot), so the recorded binds are re-inspected and persisted
// before the proxy can dial them. Verification failure fails the step;
// a stale recorded bind is never served. It reports whether
// the caller should continue with the next service (true) or finish the
// step inline (false, reserved for future fallible publishes).
func (s *Service) ensureServiceRunning(ctx context.Context, app, opID, name string, eff domain.AppEffectiveService, step *domain.AppOperationStep, result *LifecycleResult) bool {
	if eff.Container != "" {
		// Boot recovery refuses a generation whose recovery is durably
		// inhibited: a replacement may already have written to its
		// volume, so reviving this ID could corrupt the newer data.
		inhibited, err := s.recoveryInhibited(ctx, app, name, eff.Container)
		if err != nil {
			s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
			return true
		}
		if inhibited {
			s.failServiceStep(ctx, app, name, eff, step, result,
				fmt.Sprintf("recovery inhibited for container %s", eff.Container))
			return true
		}
		if ok, err := s.deps.Runtime.IsContainerRunning(ctx, eff.Container); err == nil && ok {
			return s.verifyRunningService(ctx, app, name, eff, step, result)
		}
		// Restart by exact container ID when the runtime still has it.
		if err := s.deps.Runtime.StartContainer(ctx, eff.Container); err == nil {
			return s.verifyRunningService(ctx, app, name, eff, step, result)
		}
	}
	// Otherwise redeploy from the pinned active digest.
	rev, err := s.deps.State.LoadRevision(ctx, app, eff.EffectiveRevision)
	if err != nil {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		result.Services[name] = ServiceResult{Result: "failed", Before: eff.Container, Error: err.Error()}
		return true
	}
	runtimeImage, err := s.preflightImage(ctx, eff.Spec.Image, eff.Digest)
	if err != nil {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		result.Services[name] = ServiceResult{Result: "failed", Before: eff.Container, Error: err.Error()}
		return true
	}
	pinned := pinnedService{
		name: name, spec: eff.Spec, digest: eff.Digest, runtimeImage: runtimeImage, appEnv: maps.Clone(rev.Spec.Env),
		sharedNetworks: domain.AppServiceSharedNetworks(rev.Spec, name),
	}
	svcResult, _ := s.deployService(ctx, app, rev.Revision, pinned, opID, eff.Container, serviceStopGrace(eff))
	if svcResult.Result == "failed" {
		step.State = domain.AppStepFailed
		step.Error = svcResult.Error
		step.Diagnostics = svcResult.Diagnostics
		result.Services[name] = svcResult
		return true
	}
	result.Services[name] = svcResult
	// The step is recorded as succeeded only after the effective state is
	// published: a journaled success is never ahead of what the proxy can
	// reach.
	if err := s.publishService(ctx, app, rev.Revision, pinned, svcResult, opID); err != nil {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		result.Services[name] = ServiceResult{Result: "failed", Before: eff.Container, Error: err.Error()}
		return true
	}
	// The replaced container is only retired once the replacement is
	// published: it must not be removed while a crash could leave the
	// service with no routable generation at all.
	if svcResult.Retire != "" {
		s.retireAfterPublish(ctx, app, pinned, &svcResult, result)
	}
	step.State = domain.AppStepSucceeded
	step.After = svcResult.After
	return true
}

// retireAfterPublish retires the container a published replacement
// superseded. The replaced generation keeps its inhibition: the new
// generation's publication already cleared it, and a still-unpublished
// replacement never reaches here.
func (s *Service) retireAfterPublish(ctx context.Context, app string, p pinnedService, svcResult *ServiceResult, result *LifecycleResult) {
	retired := s.retireContainer(ctx, app, retireOptions{
		Service: p.name, Grace: svcResult.RetireGrace,
	}, svcResult.Retire)
	service := result.Services[p.name]
	service.CleanupWarnings = append(service.CleanupWarnings, retired.Warnings...)
	result.Services[p.name] = service
}

// verifyRunningService withdraws a running or freshly started generation,
// re-inspects its loopback binds, and verifies readiness before the service
// may be published again. Any failure leaves the service withdrawn with its
// recorded binds cleared, so a not-ready backend is never projected.
func (s *Service) verifyRunningService(ctx context.Context, app, name string, eff domain.AppEffectiveService, step *domain.AppOperationStep, result *LifecycleResult) bool {
	if err := s.withdrawForRecovery(ctx, app, name); err != nil {
		s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
		return true
	}
	binds, udpBinds, err := s.refreshBackendBinds(ctx, app, name, eff)
	if err != nil {
		s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
		return true
	}
	if err := waitServiceReadyWithDeps(ctx, s.probeDeps(), eff.Container, eff.Spec, binds); err != nil {
		s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
		return true
	}
	// The service is running and its re-inspected loopback binds are
	// persisted, so the step may succeed: the graph apply for this app
	// follows once, after the loop.
	step.State = domain.AppStepSucceeded
	step.After = eff.Container
	result.Services[name] = ServiceResult{Result: "deployed", Before: eff.Container, After: eff.Container, BackendBinds: binds, UDPBackendBinds: udpBinds}
	return true
}

// refreshBackendBinds re-inspects the loopback publishes of a restarted
// container and persists them to the active record when they changed.
// Ephemeral binds may shift across runtime restarts; the proxy must
// never dial the stale recorded bind. Uses the non-destructive
// inspectBackendBinds: an existing ACTIVE container is never stopped
// or removed by re-inspection.
func (s *Service) refreshBackendBinds(ctx context.Context, app, name string, eff domain.AppEffectiveService) (map[int]int, map[int]int, error) {
	ports := backendPorts(eff.Spec)
	if len(ports) == 0 {
		return nil, nil, nil
	}
	binds, udpBinds, err := s.inspectBackendBinds(ctx, app, name, eff.Container, ports)
	if err != nil {
		return nil, nil, err
	}
	if !equalBinds(binds, eff.BackendBinds) || !equalBinds(udpBinds, eff.UDPBackendBinds) {
		active, ok, err := s.deps.State.LoadActive(ctx, app)
		if err != nil {
			return nil, nil, fmt.Errorf("deployment: reload active for bind refresh: %w", err)
		}
		if !ok {
			return nil, nil, fmt.Errorf("deployment: app %q has no active state: %w", app, domain.ErrAppStateConflict)
		}
		eff.BackendBinds = binds
		eff.UDPBackendBinds = udpBinds
		active.Services[name] = eff
		if err := s.deps.State.SaveActive(ctx, active); err != nil {
			return nil, nil, fmt.Errorf("deployment: persist refreshed binds: %w", err)
		}
	}
	return binds, udpBinds, nil
}

// failServiceStep records a failed service step and withdraws its
// recorded backend binds from ACTIVE: a bind that failed verification
// (or could not be refreshed) must never be served again — the
// projected backend would otherwise point at a dead or recycled
// loopback port. Invalidation errors are logged, never fatal: the
// step already failed, and the next rebuild projects fail-closed.
func (s *Service) failServiceStep(ctx context.Context, app, name string, eff domain.AppEffectiveService, step *domain.AppOperationStep, result *LifecycleResult, errMsg string) {
	step.State = domain.AppStepFailed
	step.Error = errMsg
	result.Services[name] = ServiceResult{Result: "failed", Before: eff.Container, Error: errMsg}
	s.clearServiceBinds(ctx, app, name)
}

// clearServiceBinds withdraws a service's recorded backend binds from
// ACTIVE through the canonical traffic boundary: a bind that failed
// verification, refresh, or readiness must never be served, because the
// projected backend would point at a dead, unready, or recycled loopback
// port. No graph is applied here: the caller already owns publication
// (or none is due). Errors are logged, never fatal: the step already
// failed and the next rebuild projects fail-closed.
func (s *Service) clearServiceBinds(ctx context.Context, app, name string) {
	if s.deps.Traffic == nil {
		return
	}
	if err := s.deps.Traffic.WithdrawServiceState(ctx, app, name); err != nil {
		log := zerowrap.FromCtx(ctx)
		log.Warn().Err(err).Str("app", app).Str("service", name).Msg("deployment: failed to withdraw unverified binds")
	}
}

func equalBinds(a, b map[int]int) bool {
	if len(a) != len(b) {
		return false
	}
	for port, host := range a {
		if b[port] != host {
			return false
		}
	}
	return true
}
func sortedServiceNames(active domain.AppActive) []string {
	names := make([]string, 0, len(active.Services))
	for name := range active.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// lifecycleCtx tags lifecycle use-case logs.
func lifecycleCtx(ctx context.Context, useCase, app string) context.Context {
	return zerowrap.CtxWithFields(ctx, map[string]any{
		zerowrap.FieldLayer:   "usecase",
		zerowrap.FieldUseCase: useCase,
		"app":                 app,
	})
}
