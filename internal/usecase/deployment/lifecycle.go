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
	// Warnings are operation-level leftovers that belong to no single
	// service, such as a private network that could not be verified or
	// removed.
	Warnings []CleanupWarning
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
	if err := s.reconcileInterruptedDeploy(ctx, app); err != nil {
		return nil, err
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
		step, warnings, err := s.stopService(ctx, app, name, active.Services[name])
		op.Steps = append(op.Steps, step)
		if err != nil {
			result.Services[name] = ServiceResult{Result: "failed", Before: container, Error: err.Error(), CleanupWarnings: warnings}
			failures = append(failures, name)
			continue
		}
		result.Services[name] = ServiceResult{Result: "deployed", Before: container, After: "", CleanupWarnings: warnings}
	}
	op.Outcome = ComputeOutcome(result.Services)
	op.Warnings = journalWarnings(collectCleanupWarnings(result.Services))
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
func (s *Service) stopService(ctx context.Context, app, name string, eff domain.AppEffectiveService) (domain.AppOperationStep, []CleanupWarning, error) {
	container := eff.Container
	step := domain.AppOperationStep{ID: "service." + name + ".stop", State: domain.AppStepPending, Before: container}
	fail := func(err error) (domain.AppOperationStep, []CleanupWarning, error) {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		return step, nil, err
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
		step.State = domain.AppStepSucceeded
		return step, retired.Warnings, nil
	}
	step.State = domain.AppStepSucceeded
	return step, nil, nil
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
	if err := s.reconcileInterruptedDeploy(ctx, app); err != nil {
		return nil, err
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
		// The step is journaled before the runtime work, so a candidate
		// created by a redeploy is durably recorded as soon as it exists.
		op.Steps = append(op.Steps, domain.AppOperationStep{
			ID: "service." + name + ".start", State: domain.AppStepPending, Before: eff.Container, Service: name,
		})
		s.ensureServiceRunning(ctx, app, opID, name, eff, &op, len(op.Steps)-1, result)
	}
	op.Outcome = ComputeOutcome(result.Services)
	op.Warnings = journalWarnings(collectCleanupWarnings(result.Services))
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
// service means all services in sorted order. Every service restarts in
// place: traffic is withdrawn, the same pinned container is restarted, its
// readiness probe is checked, and traffic is republished. No second
// container is created and the pinned digest never changes.
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
	if err := s.reconcileInterruptedDeploy(ctx, app); err != nil {
		return nil, err
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
		// The step is journaled before the runtime work, so a candidate
		// created by a rebuild of a missing generation is durably recorded.
		op.Steps = append(op.Steps, domain.AppOperationStep{
			ID: "service." + name + ".restart", State: domain.AppStepPending, Before: eff.Container, Service: name,
		})
		svcResult := s.restartOneService(ctx, app, op.Op, name, eff, &op, len(op.Steps)-1)
		result.Services[name] = svcResult
		if svcResult.Result != "deployed" {
			failures = append(failures, name)
		}
	}
	op.Outcome = ComputeOutcome(result.Services)
	op.Warnings = journalWarnings(collectCleanupWarnings(result.Services))
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
// cleared, so a failed restart is never served. A generation whose recorded
// container no longer exists is rebuilt from the pinned ACTIVE digest, so a
// failed replacement cannot leave restart as a permanent dead end.
func (s *Service) restartOneService(ctx context.Context, app, opID, name string, eff domain.AppEffectiveService, op *domain.AppOperation, index int) ServiceResult {
	step := &op.Steps[index]
	result := ServiceResult{Result: "failed", Before: eff.Container, After: eff.Container}
	fail := func(msg string) ServiceResult {
		step.State = domain.AppStepFailed
		step.Error = msg
		result.Result = "failed"
		result.Error = msg
		return result
	}
	if eff.Container == "" {
		result.After = ""
		return fail("no active container")
	}
	// Fail closed before any runtime mutation if a bind's policy was
	// revoked or became invalid since ACTIVE was published.
	if _, err := s.resolveServiceBinds(app, eff.Spec); err != nil {
		return fail(err.Error())
	}
	// A device grant revoked since ACTIVE was published must fail
	// before any runtime mutation, under the same rule as binds. An
	// in-place restart never rewrites device configuration.
	if _, err := s.resolveServiceDevices(app, eff.Spec); err != nil {
		return fail(err.Error())
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
		if errors.Is(err, domain.ErrContainerNotFound) {
			// The recorded generation is gone: rebuild and publish it from the
			// pinned ACTIVE digest instead of leaving the service withdrawn
			// until a deploy.
			svcResult, rebuildErr := s.redeployPinned(ctx, app, opID, name, eff, s.journalCandidate(op, index))
			if rebuildErr != nil {
				svcResult.Error = rebuildErr.Error()
				step.State = domain.AppStepFailed
				step.Error = rebuildErr.Error()
				step.Diagnostics = svcResult.Diagnostics
				// The recorded container is proven gone: the inhibition the
				// rebuild wrote for it protects nothing, and keeping it would
				// refuse every later start, recovery pass, and restart.
				if clearErr := s.clearRecoveryInhibition(ctx, app, name, eff.Container); clearErr != nil {
					svcResult.CleanupWarnings = append(svcResult.CleanupWarnings, CleanupWarning{
						Service: name, Leftover: eff.Container, Detail: "clear recovery inhibition: " + clearErr.Error(),
					})
				}
				return svcResult
			}
			step.State = domain.AppStepSucceeded
			step.After = svcResult.After
			return svcResult
		}
		s.clearServiceBinds(ctx, app, name)
		return fail(err.Error())
	}
	// Ephemeral loopback binds are not guaranteed stable across a runtime
	// restart: re-inspect before probing so the proxy never dials a stale
	// bind.
	binds, udpBinds, err := s.refreshBackendBinds(ctx, app, name, eff, s.deps.Traffic != nil)
	if err != nil {
		s.clearServiceBinds(ctx, app, name)
		return fail(err.Error())
	}
	if err := s.waitServiceReady(ctx, app, eff.Container, deploymentReadiness(eff.Spec), binds); err != nil {
		s.clearServiceBinds(ctx, app, name)
		return fail(err.Error())
	}
	step.State = domain.AppStepSucceeded
	step.After = eff.Container
	return ServiceResult{Result: "deployed", Before: eff.Container, After: eff.Container, BackendBinds: binds, UDPBackendBinds: udpBinds}
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
	if err := s.reconcileInterruptedDeploy(ctx, app); err != nil {
		return nil, err
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
	// Every exact container is confirmed gone, so the app's private
	// networks are reclaimed immediately, while the ownership record is
	// still live and its UUID can be verified against the runtime names
	// and labels. Shared networks stay, and retained volumes, secrets, and
	// images are never touched.
	ownership, err := s.deps.State.LoadOwnership(ctx, app)
	if err != nil {
		retireErr := fmt.Errorf("deployment: load ownership before reclamation: %w", err)
		s.failOperation(ctx, &op, retireErr, "error")
		return nil, retireErr
	}
	networkWarnings, err := s.reclaimPrivateNetworks(ctx, app, ownership)
	if err != nil {
		s.failOperation(ctx, &op, err, "error")
		return nil, err
	}
	result.Warnings = append(result.Warnings, networkWarnings...)
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
	op.Warnings = journalWarnings(append(collectCleanupWarnings(result.Services), result.Warnings...))
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
			result.Services[name] = ServiceResult{Result: "deployed", Before: container, After: "", CleanupWarnings: retired.Warnings}
		} else {
			result.Services[name] = ServiceResult{Result: "deployed", Before: container, After: ""}
		}
		step.State = domain.AppStepSucceeded
		steps = append(steps, step)
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
		// An interrupted operation is reconciled even when ACTIVE carries no
		// service: a first deployment can be interrupted between creating its
		// candidate and publishing it. No start follows here, so this is the
		// only reconciliation of the boot pass.
		return s.reconcileInterruptedDeploy(ctx, app)
	}
	if intent.Stopped {
		// A stopped app does not start, so its unfinished operation is
		// reconciled here instead.
		if err := s.reconcileInterruptedDeploy(ctx, app); err != nil {
			return err
		}
		for _, name := range sortedServiceNames(active) {
			if err := s.convergeStoppedService(ctx, app, name, active.Services[name]); err != nil {
				return fmt.Errorf("deployment: boot stopped convergence %q/%q: %w", app, name, err)
			}
		}
		return nil
	}
	// startLocked reconciles the app's unfinished operation before it starts
	// anything, so the boot pass reconciles exactly once.
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
// a stale recorded bind is never served. A recorded container that no
// longer exists is rebuilt from the revision pinned in ACTIVE.
func (s *Service) ensureServiceRunning(ctx context.Context, app, opID, name string, eff domain.AppEffectiveService, op *domain.AppOperation, index int, result *LifecycleResult) {
	step := &op.Steps[index]
	if eff.Container != "" {
		// Fail closed before any runtime mutation if a bind's policy was
		// revoked or became invalid since ACTIVE was published.
		if _, err := s.resolveServiceBinds(app, eff.Spec); err != nil {
			s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
			return
		}
		// Revoked device grants fail before any runtime mutation.
		if _, err := s.resolveServiceDevices(app, eff.Spec); err != nil {
			s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
			return
		}
		// Boot recovery refuses a generation whose recovery is durably
		// inhibited: a replacement may already have written to its
		// volume, so reviving this ID could corrupt the newer data.
		inhibited, err := s.recoveryInhibited(ctx, app, name, eff.Container)
		if err != nil {
			s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
			return
		}
		if inhibited {
			s.failServiceStep(ctx, app, name, eff, step, result,
				fmt.Sprintf("recovery inhibited for container %s", eff.Container))
			return
		}
		if ok, err := s.deps.Runtime.IsContainerRunning(ctx, eff.Container); err == nil && ok {
			s.verifyRunningService(ctx, app, name, eff, step, result)
			return
		}
		// Restart by exact container ID when the runtime still has it.
		if err := s.deps.Runtime.StartContainer(ctx, eff.Container); err == nil {
			s.verifyRunningService(ctx, app, name, eff, step, result)
			return
		}
	}
	// Otherwise rebuild from the revision pinned in ACTIVE.
	svcResult, err := s.redeployPinned(ctx, app, opID, name, eff, s.journalCandidate(op, index))
	if err != nil {
		step.State = domain.AppStepFailed
		step.Error = err.Error()
		step.Diagnostics = svcResult.Diagnostics
		result.Services[name] = svcResult
		return
	}
	step.State = domain.AppStepSucceeded
	step.After = svcResult.After
	result.Services[name] = svcResult
}

// redeployPinned rebuilds one service from the revision pinned in ACTIVE and
// publishes it. It is the shared recovery path for a recorded container that
// no longer exists: boot/start recovery, and a restart of a missing
// generation. The returned result is always populated, so a caller can
// journal the attempt even when err is non-nil.
func (s *Service) redeployPinned(ctx context.Context, app, opID, name string, eff domain.AppEffectiveService, journal candidateJournal) (ServiceResult, error) {
	rev, err := s.deps.State.LoadRevision(ctx, app, eff.EffectiveRevision)
	if err != nil {
		return failedServiceResult(eff.Container, err), err
	}
	runtimeImage, err := s.preflightImage(ctx, eff.Spec.Image, eff.Digest)
	if err != nil {
		return failedServiceResult(eff.Container, err), err
	}
	if _, err := s.resolveServiceBinds(app, eff.Spec); err != nil {
		return failedServiceResult(eff.Container, err), err
	}
	// Revoked device grants fail before any runtime mutation.
	if _, err := s.resolveServiceDevices(app, eff.Spec); err != nil {
		return failedServiceResult(eff.Container, err), err
	}
	pinned := pinnedService{
		name: name, spec: eff.Spec, digest: eff.Digest, runtimeImage: runtimeImage, appEnv: maps.Clone(rev.Spec.Env),
		appNetworks:    append([]domain.AppSharedNetwork(nil), rev.Spec.Networks...),
		sharedNetworks: domain.AppServiceSharedNetworks(rev.Spec, name),
	}
	svcResult := s.deployService(ctx, app, rev.Revision, pinned, opID, eff.Container, serviceStopGrace(eff), journal)
	if svcResult.Result == "failed" {
		return svcResult, errors.New(svcResult.Error)
	}
	// The step may only be recorded as succeeded once the effective state is
	// published: a journaled success is never ahead of what the proxy reaches.
	if err := s.publishService(ctx, app, rev.Revision, pinned, svcResult, opID); err != nil {
		svcResult.Result = "failed"
		svcResult.Error = err.Error()
		return svcResult, err
	}
	return svcResult, nil
}

// failedServiceResult builds the terminal result of a service step that
// failed before any container existed.
func failedServiceResult(before string, err error) ServiceResult {
	return ServiceResult{Result: "failed", Before: before, Error: err.Error()}
}

// verifyRunningService withdraws a running or freshly started generation,
// re-inspects its loopback binds, and verifies readiness before the service
// may be published again. Any failure leaves the service withdrawn with its
// recorded binds cleared, so a not-ready backend is never projected.
func (s *Service) verifyRunningService(ctx context.Context, app, name string, eff domain.AppEffectiveService, step *domain.AppOperationStep, result *LifecycleResult) {
	if err := s.withdrawForRecovery(ctx, app, name); err != nil {
		s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
		return
	}
	binds, udpBinds, err := s.refreshBackendBinds(ctx, app, name, eff, s.deps.Traffic != nil)
	if err != nil {
		s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
		return
	}
	if err := s.waitServiceReady(ctx, app, eff.Container, eff.Spec, binds); err != nil {
		s.failServiceStep(ctx, app, name, eff, step, result, err.Error())
		return
	}
	// The service is running and its re-inspected loopback binds are
	// persisted, so the step may succeed: the graph apply for this app
	// follows once, after the loop.
	step.State = domain.AppStepSucceeded
	step.After = eff.Container
	result.Services[name] = ServiceResult{Result: "deployed", Before: eff.Container, After: eff.Container, BackendBinds: binds, UDPBackendBinds: udpBinds}
}

// refreshBackendBinds re-inspects the loopback publishes of a restarted
// container and persists them to the current active record when needed.
// Ephemeral binds may shift across runtime restarts; the proxy must
// never dial the stale recorded bind. Uses the non-destructive
// inspectBackendBinds: an existing ACTIVE container is never stopped
// or removed by re-inspection.
func (s *Service) refreshBackendBinds(ctx context.Context, app, name string, eff domain.AppEffectiveService, forcePersist bool) (map[int]int, map[int]int, error) {
	ports := backendPorts(eff.Spec)
	if len(ports) == 0 {
		return nil, nil, nil
	}
	binds, udpBinds, err := s.inspectBackendBinds(ctx, app, name, eff.Container, ports)
	if err != nil {
		return nil, nil, err
	}
	if !forcePersist && equalBinds(binds, eff.BackendBinds) && equalBinds(udpBinds, eff.UDPBackendBinds) {
		return binds, udpBinds, nil
	}
	active, ok, err := s.deps.State.LoadActive(ctx, app)
	if err != nil {
		return nil, nil, fmt.Errorf("deployment: reload active for bind refresh: %w", err)
	}
	if !ok {
		return nil, nil, fmt.Errorf("deployment: app %q has no active state: %w", app, domain.ErrAppStateConflict)
	}
	if err := s.persistBackendBinds(ctx, active, name, eff, binds, udpBinds, forcePersist); err != nil {
		return nil, nil, err
	}
	return binds, udpBinds, nil
}

func (s *Service) persistBackendBinds(ctx context.Context, active domain.AppActive, name string, eff domain.AppEffectiveService, binds, udpBinds map[int]int, force bool) error {
	current, exists := active.Services[name]
	if exists && current.Container != eff.Container {
		return fmt.Errorf("deployment: active generation changed during bind refresh: %w", domain.ErrAppStateConflict)
	}
	if !force && exists && equalBinds(binds, current.BackendBinds) && equalBinds(udpBinds, current.UDPBackendBinds) {
		return nil
	}
	if !exists {
		current = eff
	}
	current.BackendBinds = binds
	current.UDPBackendBinds = udpBinds
	active.Services[name] = current
	if err := s.deps.State.SaveActive(ctx, active); err != nil {
		return fmt.Errorf("deployment: persist refreshed binds: %w", err)
	}
	return nil
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
